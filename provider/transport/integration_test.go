//go:build integration

package transport

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Integration tests against a real OpenSSH server in a Debian 13 container.
// Run with: go test -tags integration ./transport/
const debianImage = "pulumi-homelab-transport-sshd:debian13"

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func startDebianSSHD(t *testing.T, authorized ssh.PublicKey) (Hop, string) {
	t.Helper()
	docker(t, "build", "-q", "-t", debianImage, "testdata/debian13-sshd")
	id := docker(t, "run", "-d", "--rm", "-p", "127.0.0.1::22",
		"-e", "AUTHORIZED_KEY="+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authorized))), debianImage)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	_, portStr, err := net.SplitHostPort(docker(t, "port", id, "22/tcp"))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	var hostKey string
	for i := 0; i < 50; i++ {
		if out, err := exec.Command("docker", "exec", id, "cat", "/etc/ssh/ssh_host_ed25519_key.pub").Output(); err == nil {
			if c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
				c.Close()
				hostKey = string(out)
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if hostKey == "" {
		t.Fatal("sshd in the container did not come up")
	}
	f := strings.Fields(hostKey)
	return Hop{Address: "127.0.0.1", Port: port, User: "chris"}, fmt.Sprintf("[127.0.0.1]:%d %s %s", port, f[0], f[1])
}

func TestDebian13SSHD(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	hop, pinLine := startDebianSSHD(t, user.PublicKey())
	tr := newT(t, Config{Hops: []Hop{hop}, AgentSocket: startAgent(t, userPEM), HostKeys: []string{pinLine}, StrictHostKeyChecking: true})

	if out, _, code := run(t, tr, "whoami", nil); out != "chris\n" || code != 0 {
		t.Fatalf("whoami: %q %d", out, code)
	}
	if _, _, code := run(t, tr, "exit 7", nil); code != 7 {
		t.Fatalf("exit code %d", code)
	}
	big := bytes.Repeat([]byte("x"), 3<<20)
	if out, _, _ := run(t, tr, "wc -c", big); strings.TrimSpace(out) != strconv.Itoa(len(big)) {
		t.Fatalf("wc -c: %q", out)
	}
	if out, _, code := run(t, tr, "sudo -n id -u", nil); out != "0\n" || code != 0 {
		t.Fatalf("sudo: %q %d", out, code)
	}
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, code, err := tr.Run(context.Background(), "sleep 0.2", nil); err != nil || code != 0 {
				t.Errorf("concurrent run: %v %d", err, code)
			}
		}()
	}
	wg.Wait()

	_, otherPEM := newKey(t)
	bad := newT(t, Config{Hops: []Hop{hop}, AgentSocket: startAgent(t, otherPEM), HostKeys: []string{pinLine}, StrictHostKeyChecking: true})
	if _, _, _, err := bad.Run(context.Background(), "true", nil); err == nil {
		t.Fatal("an unauthorised agent key logged in")
	}
}
