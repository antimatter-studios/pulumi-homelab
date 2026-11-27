// Package transport runs commands on a remote machine over ssh, using
// golang.org/x/crypto/ssh.
//
// Everything the transport needs is passed in through Config. The package never
// reads environment variables, the home directory, ~/.ssh (config, known_hosts or
// key files) or SSH_AUTH_SOCK, and it never falls back from one credential source
// to another: a configured agent socket that is missing, unreachable or empty is
// an error, as is a host key that does not match its pin.
package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// DefaultMaxSessions keeps concurrent sessions on one connection below
// OpenSSH's default MaxSessions of 10.
const DefaultMaxSessions = 9

// DefaultConnectTimeout bounds each hop's TCP connect and ssh handshake.
const DefaultConnectTimeout = 30 * time.Second

// Hop is one machine on the route. The last hop in Config.Hops is the target;
// any before it are jump hosts, connected in order.
type Hop struct {
	Address string
	Port    int
	User    string
}

func (h Hop) hostPort() string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(h.Address, strconv.Itoa(port))
}

func (h Hop) String() string { return h.User + "@" + h.hostPort() }

// Config is the complete description of how to reach one machine.
type Config struct {
	// Hops is the route: jump hosts in order, then the target. Required.
	Hops []Hop
	// AgentSocket is the path of the ssh agent's unix socket. The caller
	// resolves it (from provider configuration or a named env var); this
	// package never looks at SSH_AUTH_SOCK.
	AgentSocket string
	// Identity is an optional private key in PEM form (unencrypted).
	Identity []byte
	// HostKeys are pinned host keys as known_hosts lines, for the target and
	// every jump host. Hashed host names are not supported.
	HostKeys []string
	// StrictHostKeyChecking refuses a host that has no pinned key. A host
	// with pinned keys must match one of them whatever this is set to.
	StrictHostKeyChecking bool
	// ConnectTimeout bounds each hop's connect and handshake. Zero means
	// DefaultConnectTimeout.
	ConnectTimeout time.Duration
	// MaxSessions bounds concurrent sessions on the connection. Zero means
	// DefaultMaxSessions.
	MaxSessions int
}

type pin struct {
	hosts []string
	key   ssh.PublicKey
}

// Transport holds one ssh connection to one machine, opened on first use and
// reused by every Run. It is safe for concurrent use.
type Transport struct {
	cfg  Config
	pins []pin
	auth []ssh.AuthMethod
	sem  chan struct{}

	mu      sync.Mutex
	clients []*ssh.Client // jump hosts, then the target last
	agentNC net.Conn
}

// New validates cfg and returns a Transport. It does not connect.
func New(cfg Config) (*Transport, error) {
	if len(cfg.Hops) == 0 {
		return nil, errors.New("transport: no hops configured: the route needs at least the target")
	}
	for i, h := range cfg.Hops {
		if h.Address == "" || h.User == "" {
			return nil, fmt.Errorf("transport: hop %d needs both an address and a user", i)
		}
	}
	if cfg.AgentSocket == "" && len(cfg.Identity) == 0 {
		return nil, errors.New("transport: no credentials configured: set an agent socket or an explicit identity")
	}
	t := &Transport{cfg: cfg}
	for i, line := range cfg.HostKeys {
		rest := []byte(line)
		for len(bytes.TrimSpace(rest)) > 0 {
			marker, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
			if err != nil {
				return nil, fmt.Errorf("transport: host key %d: %w", i, err)
			}
			rest = r
			if marker != "" {
				return nil, fmt.Errorf("transport: host key %d: marker %q is not supported", i, marker)
			}
			t.pins = append(t.pins, pin{hosts: hosts, key: key})
		}
	}
	if cfg.ConnectTimeout == 0 {
		t.cfg.ConnectTimeout = DefaultConnectTimeout
	}
	n := cfg.MaxSessions
	if n <= 0 {
		n = DefaultMaxSessions
	}
	t.sem = make(chan struct{}, n)
	return t, nil
}

// signers returns the keys from exactly the configured sources. Any problem
// with a configured source is an error; nothing else is tried.
func (t *Transport) signers() ([]ssh.Signer, error) {
	var out []ssh.Signer
	if t.cfg.AgentSocket != "" {
		nc, err := net.DialTimeout("unix", t.cfg.AgentSocket, t.cfg.ConnectTimeout)
		if err != nil {
			return nil, fmt.Errorf("transport: ssh agent at %s is unreachable (%v): unlock trove or fix the agent", t.cfg.AgentSocket, err)
		}
		ag := agent.NewClient(nc)
		ss, err := ag.Signers()
		if err != nil {
			nc.Close()
			return nil, fmt.Errorf("transport: ssh agent at %s: listing keys: %v: unlock trove or fix the agent", t.cfg.AgentSocket, err)
		}
		if len(ss) == 0 {
			nc.Close()
			return nil, fmt.Errorf("transport: ssh agent at %s holds no keys: unlock trove or fix the agent", t.cfg.AgentSocket)
		}
		t.agentNC = nc
		out = append(out, ss...)
	}
	if len(t.cfg.Identity) > 0 {
		s, err := ssh.ParsePrivateKey(t.cfg.Identity)
		if err != nil {
			return nil, fmt.Errorf("transport: explicit identity: %w", err)
		}
		out = append(out, s)
	}
	return out, nil
}

// pinNames is how known_hosts names hop: bare on port 22, else [host]:port.
func pinNames(hop Hop) []string {
	if hop.Port != 0 && hop.Port != 22 {
		return []string{"[" + hop.Address + "]:" + strconv.Itoa(hop.Port)}
	}
	return []string{hop.Address}
}

// hostKeyAlgorithms asks the server for the key types pinned for hop, so a
// server holding several host keys presents the pinned one. Nil when hop has
// no pin, leaving the library's defaults.
func (t *Transport) hostKeyAlgorithms(hop Hop) []string {
	var algs []string
	seen := map[string]bool{}
	add := func(a string) {
		if !seen[a] {
			seen[a] = true
			algs = append(algs, a)
		}
	}
	for _, p := range t.pins {
		if !matchHost(p.hosts, pinNames(hop)) {
			continue
		}
		if p.key.Type() == ssh.KeyAlgoRSA {
			add(ssh.KeyAlgoRSASHA512)
			add(ssh.KeyAlgoRSASHA256)
		}
		add(p.key.Type())
	}
	return algs
}

func (t *Transport) hostKeyCallback(hop Hop) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		names := pinNames(hop)
		pinned := false
		for _, p := range t.pins {
			if !matchHost(p.hosts, names) {
				continue
			}
			pinned = true
			if bytes.Equal(p.key.Marshal(), key.Marshal()) {
				return nil
			}
		}
		if pinned {
			return fmt.Errorf("transport: host key for %s does not match its pin (got %s %s)", hop, key.Type(), ssh.FingerprintSHA256(key))
		}
		if t.cfg.StrictHostKeyChecking {
			return fmt.Errorf("transport: no pinned host key for %s and strict host-key checking is on (it offered %s %s)", hop, key.Type(), ssh.FingerprintSHA256(key))
		}
		return nil
	}
}

func matchHost(hosts, names []string) bool {
	for _, h := range hosts {
		for _, n := range names {
			if h == n {
				return true
			}
		}
	}
	return false
}

// client returns the connection to the target, dialling the route if needed.
func (t *Transport) client(ctx context.Context) (*ssh.Client, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.clients); n > 0 {
		return t.clients[n-1], nil
	}
	if t.auth == nil {
		ss, err := t.signers()
		if err != nil {
			return nil, err
		}
		t.auth = []ssh.AuthMethod{ssh.PublicKeys(ss...)}
	}
	var clients []*ssh.Client
	fail := func(err error) (*ssh.Client, error) {
		for i := len(clients) - 1; i >= 0; i-- {
			clients[i].Close()
		}
		return nil, err
	}
	for i, hop := range t.cfg.Hops {
		cc := &ssh.ClientConfig{
			User:              hop.User,
			Auth:              t.auth,
			HostKeyCallback:   t.hostKeyCallback(hop),
			HostKeyAlgorithms: t.hostKeyAlgorithms(hop),
			Timeout:           t.cfg.ConnectTimeout,
		}
		dctx, cancel := context.WithTimeout(ctx, t.cfg.ConnectTimeout)
		var nc net.Conn
		var err error
		if i == 0 {
			var d net.Dialer
			nc, err = d.DialContext(dctx, "tcp", hop.hostPort())
		} else {
			nc, err = clients[i-1].DialContext(dctx, "tcp", hop.hostPort())
		}
		if err != nil {
			cancel()
			return fail(fmt.Errorf("transport: connecting to %s%s: %w", hop, t.via(i), err))
		}
		if dl, ok := dctx.Deadline(); ok {
			_ = nc.SetDeadline(dl)
		}
		conn, chans, reqs, err := ssh.NewClientConn(nc, hop.hostPort(), cc)
		cancel()
		if err != nil {
			nc.Close()
			return fail(fmt.Errorf("transport: ssh handshake with %s%s: %w", hop, t.via(i), err))
		}
		_ = nc.SetDeadline(time.Time{})
		c := ssh.NewClient(conn, chans, reqs)
		clients = append(clients, c)
	}
	t.clients = clients
	target := clients[len(clients)-1]
	go func() {
		_ = target.Wait()
		t.mu.Lock()
		if len(t.clients) > 0 && t.clients[len(t.clients)-1] == target {
			t.closeLocked()
		}
		t.mu.Unlock()
	}()
	return target, nil
}

func (t *Transport) via(i int) string {
	if i == 0 {
		return ""
	}
	return " via " + t.cfg.Hops[i-1].String()
}

// Run runs command through the remote user's shell, with stdin as its
// standard input. A non-zero exit is a result (code), not an error; err means
// the transport itself failed or ctx ended.
func (t *Transport) Run(ctx context.Context, command string, stdin []byte) (stdout, stderr []byte, code int, err error) {
	c, err := t.client(ctx)
	if err != nil {
		return nil, nil, -1, err
	}
	select {
	case t.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, -1, ctx.Err()
	}
	defer func() { <-t.sem }()

	s, err := t.newSession(ctx, c)
	if err != nil {
		return nil, nil, -1, err
	}
	defer s.Close()
	var out, errb bytes.Buffer
	s.Stdin = bytes.NewReader(stdin)
	s.Stdout = &out
	s.Stderr = &errb
	if err := s.Start(command); err != nil {
		return nil, nil, -1, fmt.Errorf("transport: starting command on %s: %w", t.cfg.Hops[len(t.cfg.Hops)-1], err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case <-ctx.Done():
		_ = s.Signal(ssh.SIGKILL)
		s.Close()
		<-done // the output buffers are written until Wait returns
		return out.Bytes(), errb.Bytes(), -1, ctx.Err()
	case werr := <-done:
		if werr == nil {
			return out.Bytes(), errb.Bytes(), 0, nil
		}
		var ee *ssh.ExitError
		if errors.As(werr, &ee) {
			return out.Bytes(), errb.Bytes(), ee.ExitStatus(), nil
		}
		return out.Bytes(), errb.Bytes(), -1, fmt.Errorf("transport: command on %s: %w", t.cfg.Hops[len(t.cfg.Hops)-1], werr)
	}
}

// newSession opens a session. sshd frees a session slot a little after the
// client sees the session close, so a slot this pool has just released can
// still count against MaxSessions; a refused open ("open failed") is retried
// with backoff for a short while. Nothing has run when an open is refused, so
// the retry is safe.
func (t *Transport) newSession(ctx context.Context, c *ssh.Client) (*ssh.Session, error) {
	delay := 10 * time.Millisecond
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := c.NewSession()
		if err == nil {
			return s, nil
		}
		var oce *ssh.OpenChannelError
		if !errors.As(err, &oce) || oce.Reason != ssh.ConnectionFailed || time.Now().After(deadline) {
			return nil, fmt.Errorf("transport: opening a session on %s: %w", t.cfg.Hops[len(t.cfg.Hops)-1], err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 200*time.Millisecond {
			delay *= 2
		}
	}
}

// Close closes the connection (and every jump) and the agent connection.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
	if t.agentNC != nil {
		t.agentNC.Close()
		t.agentNC = nil
		t.auth = nil
	}
	return nil
}

func (t *Transport) closeLocked() {
	for i := len(t.clients) - 1; i >= 0; i-- {
		t.clients[i].Close()
	}
	t.clients = nil
}
