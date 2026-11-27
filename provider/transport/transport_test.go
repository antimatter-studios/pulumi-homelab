package transport

import (
	"bytes"
	"context"
	"errors"
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
