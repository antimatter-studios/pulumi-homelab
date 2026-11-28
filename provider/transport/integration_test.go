//go:build integration

package transport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
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
	id := docker(t, "run", "-d", "-p", "127.0.0.1::22",
		"-e", "AUTHORIZED_KEY="+strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authorized))), debianImage)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return waitForSSHD(t, id)
}

// waitForSSHD waits for the container's sshd on its published port and returns the hop and the
// known_hosts line pinning its host key.
func waitForSSHD(t *testing.T, id string) (Hop, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "port", id, "22/tcp").CombinedOutput()
	if err != nil {
		failSSHDStartup(t, id, fmt.Sprintf("docker port: %v\n%s", err, out))
	}
	address := strings.TrimSpace(string(out))
	_, portStr, err := net.SplitHostPort(address)
	if err != nil {
		failSSHDStartup(t, id, err.Error())
	}
	port, _ := strconv.Atoi(portStr)
	const retryInterval = 200 * time.Millisecond
	for ctx.Err() == nil {
		out, err := exec.CommandContext(ctx, "docker", "exec", id, "cat", "/etc/ssh/ssh_host_ed25519_key.pub").Output()
		if fields := strings.Fields(string(out)); err == nil && len(fields) >= 2 {
			probe, cancelProbe := context.WithTimeout(ctx, retryInterval)
			ready := hasSSHBanner(probe, address)
			cancelProbe()
			if ready {
				return Hop{Address: "127.0.0.1", Port: port, User: "chris"}, fmt.Sprintf("[127.0.0.1]:%d %s %s", port, fields[0], fields[1])
			}
		}
		select {
		case <-time.After(retryInterval):
		case <-ctx.Done():
		}
	}
	failSSHDStartup(t, id, "timed out waiting for the SSH banner and host key")
	return Hop{}, ""
}

func hasSSHBanner(ctx context.Context, address string) bool {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false
	}
	defer conn.Close()

	deadline, _ := ctx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		return false
	}
	banner, err := bufio.NewReader(io.LimitReader(conn, 255)).ReadString('\n')
	return err == nil && strings.HasPrefix(banner, "SSH-2.0-")
}

func failSSHDStartup(t *testing.T, id, reason string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	logs, err := exec.CommandContext(ctx, "docker", "logs", id).CombinedOutput()
	if err != nil {
		t.Logf("docker logs %s: %v", id, err)
	}
	t.Fatalf("sshd startup failed: %s\nContainer logs:\n%s", reason, logs)
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
