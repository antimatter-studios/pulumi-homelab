//go:build integration

package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A jump host against real OpenSSH: two Debian 13 containers on a private network. Only the jump
// publishes a port; the target is reachable solely through it, by its name on that network, and its
// host key is pinned under that name.
func TestDebian13JumpHost(t *testing.T) {
	fakeHome(t)
	user, userPEM := newKey(t)
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(user.PublicKey())))

	suffix := make([]byte, 4)
	rand.Read(suffix)
	network := "pulumi-homelab-jump-" + hex.EncodeToString(suffix)
	docker(t, "network", "create", network)
	t.Cleanup(func() { exec.Command("docker", "network", "rm", network).Run() })

	docker(t, "build", "-q", "-t", debianImage, "testdata/debian13-sshd")
	target := docker(t, "run", "-d", "--rm", "--network", network, "--network-alias", "target",
		"--hostname", "target", "-e", "AUTHORIZED_KEY="+authorized, debianImage)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", target).Run() })
	jumpHop, jumpPin := startDebianSSHDOn(t, network, authorized)

	var targetKey string
	for i := 0; i < 50 && targetKey == ""; i++ {
		if out, err := exec.Command("docker", "exec", target, "cat", "/etc/ssh/ssh_host_ed25519_key.pub").Output(); err == nil {
			if exec.Command("docker", "exec", target, "sh", "-c", "pgrep -x sshd").Run() == nil {
				targetKey = string(out)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if targetKey == "" {
		t.Fatal("the target's sshd did not come up")
	}
	f := strings.Fields(targetKey)
	targetPin := "target " + f[0] + " " + f[1]
	targetHop := Hop{Address: "target", Port: 22, User: "chris"}

	tr := newT(t, Config{Hops: []Hop{jumpHop, targetHop}, AgentSocket: startAgent(t, userPEM),
		HostKeys: []string{jumpPin, targetPin}, StrictHostKeyChecking: true})
	if out, _, code := run(t, tr, "hostname", nil); out != "target\n" || code != 0 {
		t.Fatalf("through the jump, hostname = %q (exit %d), want the target", out, code)
	}
	if out, _, code := run(t, tr, "sudo -n id -u", nil); out != "0\n" || code != 0 {
		t.Fatalf("sudo through the jump: %q %d", out, code)
	}

	// The target's key is checked too: a wrong pin for it is refused even though the jump is fine.
	other, _ := newKey(t)
	wrong := "target " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(other.PublicKey())))
	bad := newT(t, Config{Hops: []Hop{jumpHop, targetHop}, AgentSocket: startAgent(t, userPEM),
		HostKeys: []string{jumpPin, wrong}, StrictHostKeyChecking: true})
	if _, _, _, err := bad.Run(context.Background(), "true", nil); err == nil {
		t.Fatal("a wrong pin for the target behind the jump was accepted")
	}
}

// startDebianSSHDOn is startDebianSSHD on a given docker network, publishing port 22 to the host.
func startDebianSSHDOn(t *testing.T, network, authorized string) (Hop, string) {
	t.Helper()
	id := docker(t, "run", "-d", "--rm", "--network", network, "-p", "127.0.0.1::22",
		"-e", "AUTHORIZED_KEY="+authorized, debianImage)
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return waitForSSHD(t, id)
}
