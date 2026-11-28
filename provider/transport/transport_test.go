package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// The Runner method that provider/remote (defined separately) expects.
var _ interface {
	Run(ctx context.Context, command string, stdin []byte) (stdout, stderr []byte, code int, err error)
} = (*Transport)(nil)

// fakeHome points HOME at a temp dir for the test, so nothing real under ~ can
// be read even by accident.
func fakeHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("SSH_AUTH_SOCK", "")
	return h
}

func newT(t *testing.T, cfg Config) *Transport {
	t.Helper()
	tr, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func run(t *testing.T, tr *Transport, cmd string, stdin []byte) (string, string, int) {
	t.Helper()
	out, errb, code, err := tr.Run(context.Background(), cmd, stdin)
	if err != nil {
		t.Fatalf("Run(%q): %v", cmd, err)
	}
	return string(out), string(errb), code
}

func TestExecExitCodesAndStdin(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})

	if out, _, code := run(t, tr, "echo hello; echo oops >&2", nil); out != "hello\n" || code != 0 {
		t.Fatalf("got %q code %d", out, code)
	}
	if _, errb, code := run(t, tr, "echo bad >&2; exit 3", nil); code != 3 || errb != "bad\n" {
		t.Fatalf("got stderr %q code %d", errb, code)
	}
	// 2 MB on stdin: no command-line length limit applies to data.
	big := bytes.Repeat([]byte("0123456789abcdef"), 128*1024)
	if out, _, code := run(t, tr, "wc -c", big); strings.TrimSpace(out) != "2097152" || code != 0 {
		t.Fatalf("wc -c: %q code %d", out, code)
	}
	if out, _, _ := run(t, tr, "cat", []byte("secret\n")); out != "secret\n" {
		t.Fatalf("cat: %q", out)
	}
	if n := d.conns.Load(); n != 1 {
		t.Fatalf("expected one reused connection, server saw %d", n)
	}
}

func TestExplicitIdentityOnly(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, Identity: userPEM, HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	if out, _, _ := run(t, tr, "echo ok", nil); out != "ok\n" {
		t.Fatalf("got %q", out)
	}
}

func TestJumpHops(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	jump1 := startSSHD(t, user.PublicKey())
	jump2 := startSSHD(t, user.PublicKey())
	target := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{
		Hops:                  []Hop{jump1.hop("admin"), jump2.hop("admin"), target.hop("chris")},
		AgentSocket:           startAgent(t, userPEM),
		HostKeys:              []string{jump1.pin(), jump2.pin(), target.pin()},
		StrictHostKeyChecking: true,
	})
	if out, _, _ := run(t, tr, "echo via", nil); out != "via\n" {
		t.Fatalf("got %q", out)
	}
	if jump1.forwards.Load() != 1 || jump2.forwards.Load() != 1 || target.conns.Load() != 1 {
		t.Fatalf("route not taken: forwards %d/%d, target conns %d", jump1.forwards.Load(), jump2.forwards.Load(), target.conns.Load())
	}
}

func TestHostKeyMismatchRejected(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	other := startSSHD(t)
	wrong := strings.Replace(other.pin(), other.ln.Addr().String()[len("127.0.0.1:"):], d.ln.Addr().String()[len("127.0.0.1:"):], 1)
	for _, strict := range []bool{true, false} {
		tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{wrong}, StrictHostKeyChecking: strict})
		_, _, _, err := tr.Run(context.Background(), "true", nil)
		if err == nil || !strings.Contains(err.Error(), "does not match its pin") || !strings.Contains(err.Error(), d.hop("chris").String()) {
			t.Fatalf("strict=%v: want a pin mismatch naming the host, got %v", strict, err)
		}
	}
}

func TestJumpHostKeyMismatchRejected(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	jump := startSSHD(t, user.PublicKey())
	target := startSSHD(t, user.PublicKey())
	// The target's pin is right; the jump has none.
	tr := newT(t, Config{Hops: []Hop{jump.hop("admin"), target.hop("chris")}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{target.pin()}, StrictHostKeyChecking: true})
	_, _, _, err := tr.Run(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "no pinned host key for "+jump.hop("admin").String()) {
		t.Fatalf("want an unpinned-jump error, got %v", err)
	}
}

func TestUnpinnedHost(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM), StrictHostKeyChecking: true})
	if _, _, _, err := tr.Run(context.Background(), "true", nil); err == nil || !strings.Contains(err.Error(), "no pinned host key") {
		t.Fatalf("strict: want refusal, got %v", err)
	}
	lax := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM)})
	if _, _, code := run(t, lax, "true", nil); code != 0 {
		t.Fatal("non-strict should accept an unpinned host")
	}
}

func TestSessionPoolUnderConcurrency(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, _, code, err := tr.Run(context.Background(), "sleep 0.05; echo x", nil)
			if err != nil || code != 0 || string(out) != "x\n" {
				errs <- errors.Join(err, errors.New("bad result: "+string(out)))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if m := d.maxSessions.Load(); m > DefaultMaxSessions || m < 2 {
		t.Fatalf("max concurrent sessions %d, want 2..%d", m, DefaultMaxSessions)
	}
	if n := d.conns.Load(); n != 1 {
		t.Fatalf("expected one connection, got %d", n)
	}
}

func TestContextCancel(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	d := startSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _, err := tr.Run(ctx, "sleep 30", nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("want a prompt deadline error, got %v after %v", err, time.Since(start))
	}
	// The connection is still usable afterwards.
	if out, _, _ := run(t, tr, "echo after", nil); out != "after\n" {
		t.Fatalf("got %q", out)
	}
}

func TestConfigErrors(t *testing.T) {
	fakeHome(t)
	for name, cfg := range map[string]Config{
		"no hops":        {AgentSocket: "/x"},
		"no user":        {Hops: []Hop{{Address: "h"}}, AgentSocket: "/x"},
		"no credentials": {Hops: []Hop{{Address: "h", User: "u"}}},
		"bad pin":        {Hops: []Hop{{Address: "h", User: "u"}}, AgentSocket: "/x", HostKeys: []string{"h not-a-key"}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// --- Booby traps: credentials outside the configuration must never be used. ---

// trapHome fills a fake HOME with everything OpenSSH would use to log in
// successfully: the authorised key as id_ed25519, a known_hosts entry, and a
// config naming the key. It also exports SSH_AUTH_SOCK pointing at an agent
// that holds the authorised key.
func trapHome(t *testing.T, d *testSSHD, keyPEM []byte) {
	t.Helper()
	home := fakeHome(t)
	dot := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dot, 0o700); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dot, "id_ed25519"), keyPEM, 0o600))
	must(os.WriteFile(filepath.Join(dot, "known_hosts"), []byte(d.pin()), 0o600))
	must(os.WriteFile(filepath.Join(dot, "config"), []byte("Host *\n  IdentityFile ~/.ssh/id_ed25519\n  StrictHostKeyChecking no\n"), 0o600))
	t.Setenv("SSH_AUTH_SOCK", startAgent(t, keyPEM))
	t.Setenv("CHORE_AGENT_SOCK", startAgent(t, keyPEM))
}

func assertNotOffered(t *testing.T, d *testSSHD, k ssh.PublicKey) {
	t.Helper()
	fp := ssh.FingerprintSHA256(k)
	for _, o := range d.offeredKeys() {
		if o == fp {
			t.Fatalf("the trapped key %s was offered to the server", fp)
		}
	}
}

func TestBoobyTrapAgentWithoutTheKey(t *testing.T) {
	good, goodPEM := newKey(t)
	_, otherPEM := newKey(t)
	d := startSSHD(t, good.PublicKey())
	trapHome(t, d, goodPEM)

	// A configuration equivalent to homelab-server's: one explicit agent,
	// host keys pinned, strict. The agent lacks the authorised key.
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, otherPEM), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	_, _, _, err := tr.Run(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want an authentication failure, got %v", err)
	}
	assertNotOffered(t, d, good.PublicKey())
}

func TestBoobyTrapMissingAgentSocket(t *testing.T) {
	good, goodPEM := newKey(t)
	d := startSSHD(t, good.PublicKey())
	trapHome(t, d, goodPEM)

	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: filepath.Join(t.TempDir(), "absent.sock"), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	_, _, _, err := tr.Run(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "unreachable") || !strings.Contains(err.Error(), "unlock trove") {
		t.Fatalf("want a fatal unreachable-agent error, got %v", err)
	}
	if n := d.conns.Load(); n != 0 || len(d.offeredKeys()) != 0 {
		t.Fatalf("the server was contacted (%d conns, %d keys offered)", n, len(d.offeredKeys()))
	}
}

func TestBoobyTrapEmptyAgent(t *testing.T) {
	good, goodPEM := newKey(t)
	d := startSSHD(t, good.PublicKey())
	trapHome(t, d, goodPEM)

	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t), HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	_, _, _, err := tr.Run(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "holds no keys") {
		t.Fatalf("want a fatal empty-agent error, got %v", err)
	}
	if len(d.offeredKeys()) != 0 {
		t.Fatal("keys were offered despite an empty configured agent")
	}
}

func TestBoobyTrapIdentityOnlyIgnoresAgentAndHome(t *testing.T) {
	good, goodPEM := newKey(t)
	_, otherPEM := newKey(t)
	d := startSSHD(t, good.PublicKey())
	trapHome(t, d, goodPEM)

	// Only an explicit (wrong) identity configured: SSH_AUTH_SOCK,
	// CHORE_AGENT_SOCK and ~/.ssh all hold the right key and must be ignored.
	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, Identity: otherPEM, HostKeys: []string{d.pin()}, StrictHostKeyChecking: true})
	_, _, _, err := tr.Run(context.Background(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want an authentication failure, got %v", err)
	}
	assertNotOffered(t, d, good.PublicKey())
	if len(d.offeredKeys()) != 1 {
		t.Fatalf("expected exactly the configured key to be offered, got %d keys", len(d.offeredKeys()))
	}
}

func TestBoobyTrapKnownHostsNotUsed(t *testing.T) {
	good, goodPEM := newKey(t)
	d := startSSHD(t, good.PublicKey())
	trapHome(t, d, goodPEM) // ~/.ssh/known_hosts pins d correctly

	tr := newT(t, Config{Hops: []Hop{d.hop("chris")}, AgentSocket: startAgent(t, goodPEM), StrictHostKeyChecking: true})
	if _, _, _, err := tr.Run(context.Background(), "true", nil); err == nil || !strings.Contains(err.Error(), "no pinned host key") {
		t.Fatalf("~/.ssh/known_hosts must not satisfy strict checking, got %v", err)
	}
}

func TestDialFailureReturnsAnError(t *testing.T) {
	_, identity := newKey(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hop := Hop{Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "chris"}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	tr := newT(t, Config{Hops: []Hop{hop}, Identity: identity, ConnectTimeout: time.Second})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, code, err := tr.Run(ctx, "cat", []byte("file contents"))
	if err == nil || code != -1 || !strings.Contains(err.Error(), "connecting to "+hop.String()) || ctx.Err() != nil {
		t.Fatalf("want a prompt connection error for %s, got exit %d, %v", hop, code, err)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	user, identity := newKey(t)
	for _, useJump := range []bool{false, true} {
		name := "direct"
		if useJump {
			name = "through a jump"
		}
		t.Run(name, func(t *testing.T) {
			if useJump {
				t.Skip("blocked by #136: the SSH channel does not enforce the handshake deadline")
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { listener.Close() })
			target := Hop{Address: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "chris"}
			done := make(chan struct{})
			defer func() {
				listener.Close()
				<-done
			}()
			// A server that accepts TCP but never sends an SSH banner.
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				_, _ = io.Copy(io.Discard, conn)
			}()

			cfg := Config{Hops: []Hop{target}, Identity: identity, ConnectTimeout: 200 * time.Millisecond}
			if useJump {
				jump := startSSHD(t, user.PublicKey())
				cfg.Hops = append([]Hop{jump.hop("admin")}, cfg.Hops...)
				cfg.HostKeys = []string{jump.pin()}
			}
			tr := newT(t, cfg)
			_, _, _, err = tr.Run(t.Context(), "true", nil)
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() || !strings.Contains(err.Error(), target.String()) {
				t.Fatalf("want handshake timeout naming %s, got %v", target, err)
			}
		})
	}
}

func TestExplicitIdentityAcrossJumpChain(t *testing.T) {
	fakeHome(t)
	user, identity := newKey(t)
	jump1 := startSSHD(t, user.PublicKey())
	jump2 := startSSHD(t, user.PublicKey())
	target := startSSHD(t, user.PublicKey())
	cfg := Config{
		Hops:     []Hop{jump1.hop("admin"), jump2.hop("relay"), target.hop("chris")},
		Identity: identity, HostKeys: []string{jump1.pin(), jump2.pin(), target.pin()}, StrictHostKeyChecking: true,
	}
	if out, _, code := run(t, newT(t, cfg), "printf '%s' \"$((6 * 7))\"", nil); out != "42" || code != 0 {
		t.Fatalf("exec through both jumps: %q, exit %d", out, code)
	}
	for _, server := range []*testSSHD{jump1, jump2, target} {
		keys := server.offeredKeys()
		if len(keys) != 1 || keys[0] != ssh.FingerprintSHA256(user.PublicKey()) {
			t.Fatalf("keys offered to port %d: %v", server.port(), keys)
		}
	}
	_, other := newKey(t)
	cfg.Identity = other
	_, _, _, err := newT(t, cfg).Run(t.Context(), "true", nil)
	if err == nil || !strings.Contains(err.Error(), cfg.Hops[0].String()) ||
		!strings.Contains(err.Error(), "attempted methods [none publickey]") {
		t.Fatalf("want public-key authentication failure naming the first jump, got %v", err)
	}
}

func TestTransportConnectionsAreIsolatedByRouteAndCredentials(t *testing.T) {
	fakeHome(t)
	user, identity := newKey(t)
	_, other := newKey(t)
	target := startSSHD(t, user.PublicKey())
	jump1 := startSSHD(t, user.PublicKey())
	jump2 := startSSHD(t, user.PublicKey())
	cfg := Config{Hops: []Hop{target.hop("chris")}, Identity: identity, HostKeys: []string{target.pin()}, StrictHostKeyChecking: true}
	for _, jump := range []*testSSHD{nil, jump1, jump2} {
		routed := cfg
		if jump != nil {
			routed.Hops = append([]Hop{jump.hop("admin")}, routed.Hops...)
			routed.HostKeys = append([]string{jump.pin()}, routed.HostKeys...)
		}
		tr := newT(t, routed)
		for range 2 {
			if _, _, code := run(t, tr, "true", nil); code != 0 {
				t.Fatalf("exit %d", code)
			}
		}
	}
	if jump1.forwards.Load() != 1 || jump2.forwards.Load() != 1 || target.conns.Load() != 3 {
		t.Fatalf("forwards %d/%d and target connections %d, want 1/1 and 3",
			jump1.forwards.Load(), jump2.forwards.Load(), target.conns.Load())
	}
	wrongKey := cfg
	wrongKey.Identity = other
	if _, _, _, err := newT(t, wrongKey).Run(t.Context(), "true", nil); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want authentication failure for a different explicit identity, got %v", err)
	}
	agentCfg := cfg
	agentCfg.Identity = nil
	agentCfg.AgentSocket = startAgent(t, identity)
	if _, _, code := run(t, newT(t, agentCfg), "true", nil); code != 0 {
		t.Fatalf("authorized agent: exit %d", code)
	}
	agentCfg.AgentSocket = startAgent(t, other)
	if _, _, _, err := newT(t, agentCfg).Run(t.Context(), "true", nil); err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Fatalf("want authentication failure for a different agent, got %v", err)
	}
}

func TestHopAddressKeepsHostAndPortSeparate(t *testing.T) {
	for _, tc := range []struct {
		hop  Hop
		want string
	}{
		{Hop{Address: "jump.example", User: "admin", Port: 2222}, "admin@jump.example:2222"},
		{Hop{Address: "jump.example", User: "admin"}, "admin@jump.example:22"},
		{Hop{Address: "host:name", User: "admin", Port: 2200}, "admin@[host:name]:2200"},
		{Hop{Address: "2001:db8::22", User: "admin", Port: 2200}, "admin@[2001:db8::22]:2200"},
	} {
		if got := tc.hop.String(); got != tc.want {
			t.Errorf("%+v: got %q, want %q", tc.hop, got, tc.want)
		}
	}
}

func TestAgentAndExplicitIdentityAreConfiguredSources(t *testing.T) {
	fakeHome(t)
	agentKey, agentPEM := newKey(t)
	explicitKey, explicitPEM := newKey(t)
	jump := startSSHD(t, agentKey.PublicKey())
	target := startSSHD(t, explicitKey.PublicKey())
	cfg := Config{
		Hops:        []Hop{jump.hop("admin"), target.hop("chris")},
		AgentSocket: startAgent(t, agentPEM), Identity: explicitPEM,
		HostKeys: []string{jump.pin(), target.pin()}, StrictHostKeyChecking: true,
	}
	if _, _, code := run(t, newT(t, cfg), "true", nil); code != 0 {
		t.Fatalf("exec through jump: exit %d", code)
	}
}
