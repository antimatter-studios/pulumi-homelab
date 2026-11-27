package transport

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// testSSHD is an in-process ssh server: exec runs the command with /bin/sh -c
// locally, direct-tcpip forwards (for jump hosts) are honoured, and every
// public key a client offers is recorded.
type testSSHD struct {
	ln         net.Listener
	hostSigner ssh.Signer
	authorized map[string]bool // marshalled public keys

	mu      sync.Mutex
	offered []string // SHA256 fingerprints offered by clients, in order

	conns       atomic.Int32
	sessions    atomic.Int32
	maxSessions atomic.Int32
	forwards    atomic.Int32
}

func newKey(t *testing.T) (ssh.Signer, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, pem.EncodeToMemory(blk)
}

func startSSHD(t *testing.T, authorized ...ssh.PublicKey) *testSSHD {
	t.Helper()
	host, _ := newKey(t)
	d := &testSSHD{hostSigner: host, authorized: map[string]bool{}}
	for _, k := range authorized {
		d.authorized[string(k.Marshal())] = true
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d.ln = ln
	t.Cleanup(func() { ln.Close() })
	go d.serve()
	return d
}

func (d *testSSHD) port() int { return d.ln.Addr().(*net.TCPAddr).Port }

// pin returns the known_hosts line for this server as seen at 127.0.0.1.
func (d *testSSHD) pin() string {
	return fmt.Sprintf("[127.0.0.1]:%d %s", d.port(), ssh.MarshalAuthorizedKey(d.hostSigner.PublicKey()))
}

func (d *testSSHD) hop(user string) Hop { return Hop{Address: "127.0.0.1", Port: d.port(), User: user} }

func (d *testSSHD) offeredKeys() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.offered...)
}

func (d *testSSHD) serve() {
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			d.mu.Lock()
			d.offered = append(d.offered, ssh.FingerprintSHA256(key))
			d.mu.Unlock()
			if d.authorized[string(key.Marshal())] {
				return nil, nil
			}
			return nil, fmt.Errorf("unauthorised key")
		},
	}
	cfg.AddHostKey(d.hostSigner)
	for {
		nc, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.handle(nc, cfg)
	}
}

func (d *testSSHD) handle(nc net.Conn, cfg *ssh.ServerConfig) {
	conn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		nc.Close()
		return
	}
	d.conns.Add(1)
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		switch nch.ChannelType() {
		case "session":
			go d.session(nch)
		case "direct-tcpip":
			go d.forward(nch)
		default:
			nch.Reject(ssh.UnknownChannelType, "no")
		}
	}
}

func (d *testSSHD) forward(nch ssh.NewChannel) {
	var p struct {
		Host     string
		Port     uint32
		OrigHost string
		OrigPort uint32
	}
	if err := ssh.Unmarshal(nch.ExtraData(), &p); err != nil {
		nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	tc, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	if err != nil {
		nch.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nch.Accept()
	if err != nil {
		tc.Close()
		return
	}
	d.forwards.Add(1)
	go ssh.DiscardRequests(reqs)
	go func() { io.Copy(ch, tc); ch.CloseWrite() }()
	io.Copy(tc, ch)
	tc.Close()
	ch.Close()
}

func (d *testSSHD) session(nch ssh.NewChannel) {
	ch, reqs, err := nch.Accept()
	if err != nil {
		return
	}
	n := d.sessions.Add(1)
	for {
		m := d.maxSessions.Load()
		if n <= m || d.maxSessions.CompareAndSwap(m, n) {
			break
		}
	}
	// The session stops counting once its exit status is decided, before that status is sent: the
	// client may open its next session as soon as it reads the status, and counting until this
	// handler's deferred cleanup ran would race with that and record one session too many.
	var once sync.Once
	done := func() { once.Do(func() { d.sessions.Add(-1) }) }
	defer done()
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			cmd := exec.Command("/bin/sh", "-c", p.Command)
			cmd.Stdin = ch
			cmd.Stdout = ch
			cmd.Stderr = ch.Stderr()
			if err := cmd.Start(); err != nil {
				return
			}
			proc := cmd.Process
			go func() {
				for r := range reqs {
					if r.Type == "signal" {
						proc.Kill()
					}
					if r.WantReply {
						r.Reply(false, nil)
					}
				}
			}()
			code := 0
			if err := cmd.Wait(); err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
					if code < 0 {
						code = 137
					}
				} else {
					code = 255
				}
			}
			status := make([]byte, 4)
			binary.BigEndian.PutUint32(status, uint32(code))
			done()
			ch.SendRequest("exit-status", false, status)
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// startAgent serves an in-memory agent holding keys on a unix socket.
func startAgent(t *testing.T, keys ...[]byte) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ag")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	kr := agent.NewKeyring()
	for _, pemKey := range keys {
		raw, err := ssh.ParseRawPrivateKey(pemKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := kr.Add(agent.AddedKey{PrivateKey: raw}); err != nil {
			t.Fatal(err)
		}
	}
	path := dir + "/agent.sock"
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { agent.ServeAgent(kr, c); c.Close() }()
		}
	}()
	return path
}
