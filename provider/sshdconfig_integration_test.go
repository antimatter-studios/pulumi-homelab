//go:build integration

package homelab

import (
	"os/exec"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// SshdConfig against a real sshd in Debian 13, reached as chris with sudo -n: a good drop-in goes
// in and takes effect, a setting sshd rejects never reaches the live tree, and a setting sshd
// accepts but cannot run with (a ListenAddress the machine does not have, which makes the
// reloaded sshd exit) is rolled back and sshd brought back. Each time the live file is compared
// byte for byte and sshd must still answer with its banner.
const sshdReloadImage = "pulumi-homelab-provider-sshd-reload:debian13"

func startSshd(t *testing.T) dockerRunner {
	t.Helper()
	if out, err := exec.Command("docker", "build", "-q", "-t", sshdReloadImage, "testdata/debian13-sshd-reload").CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", sshdReloadImage).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return dockerRunner{id: id}
}

// onRunningSshd is onDebian with sshd running and a systemctl that reloads it, which SshdConfig
// needs: it will not touch a machine whose sshd it cannot see is healthy before and after.
func onRunningSshd(t *testing.T) (lifecycleServer, func(string) string) {
	t.Helper()
	d := startSshd(t)
	return serve(t, d), func(command string) string { return d.sh(t, command) }
}

func sshdInputs(unchecked map[string]string) property.Map {
	values := map[string]property.Value{}
	for k, v := range unchecked {
		values[k] = property.New(v)
	}
	return property.NewMap(map[string]property.Value{
		"file":      property.New("90-tunnel"),
		"settings":  property.New(property.NewMap(map[string]property.Value{"x11Forwarding": property.New(false), "maxAuthTries": property.New(3.0)})),
		"unchecked": property.New(property.NewMap(values)),
	})
}

func TestSshdConfigRealSshd(t *testing.T) {
	d := startSshd(t)
	s := realServer(t, d)
	const path = "/etc/ssh/sshd_config.d/90-tunnel.conf"
	banner := func(t *testing.T) {
		t.Helper()
		got := d.sh(t, `timeout 5 bash -c 'exec 3<>/dev/tcp/127.0.0.1/22 && head -n 1 <&3'`)
		if !strings.HasPrefix(got, "SSH-2.0-") {
			t.Fatalf("sshd does not answer: %q", got)
		}
	}
	banner(t)

	created, err := s.Create(p.CreateRequest{Urn: sshdURN, Properties: sshdInputs(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got := created.Properties.Get("effective").AsMap().Get("maxauthtries").AsString(); got != "3" {
		t.Fatalf("effective maxauthtries %q", got)
	}
	good := d.sh(t, "cat "+path)
	if !strings.Contains(good, "MaxAuthTries 3") || !strings.Contains(d.sh(t, "cat /tmp/systemctl.log"), "reload ssh.service") {
		t.Fatalf("drop-in %q", good)
	}
	if out := d.sh(t, "cat /tmp/systemctl.log"); strings.Contains(out, "restart") || strings.Contains(out, "start ssh") {
		t.Fatalf("sshd restarted: %s", out)
	}
	pid := d.sh(t, "cat /run/sshd.pid")
	banner(t)

	// sshd -t rejects it: refused before anything live is touched
	_, err = s.Update(p.UpdateRequest{ID: created.ID, Urn: sshdURN, State: created.Properties, Inputs: sshdInputs(map[string]string{"PermitRootLogin": "bogus"})})
	if err == nil || !strings.Contains(err.Error(), "sshd -t rejects the configuration with this change") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("invalid setting: %v", err)
	}
	if got := d.sh(t, "cat "+path); got != good {
		t.Fatalf("live drop-in changed:\n%s", got)
	}
	if d.sh(t, "cat /run/sshd.pid") != pid {
		t.Fatal("sshd was restarted")
	}
	banner(t)

	// sshd -t accepts it, but the reloaded sshd cannot listen and exits: rolled back and revived
	_, err = s.Update(p.UpdateRequest{ID: created.ID, Urn: sshdURN, State: created.Properties, Inputs: sshdInputs(map[string]string{"ListenAddress": "192.0.2.1"})})
	if err == nil || !strings.Contains(err.Error(), "sshd is not healthy after the reload") ||
		!strings.Contains(err.Error(), "Rolled back: restored the previous "+path) || !strings.Contains(err.Error(), "sshd is healthy again") {
		t.Fatalf("lockout: %v", err)
	}
	if got := d.sh(t, "cat "+path); got != good {
		t.Fatalf("live drop-in not restored:\n%s", got)
	}
	if !strings.Contains(d.sh(t, "cat /tmp/systemctl.log"), "start ssh.service") {
		t.Fatal("the bad reload did not kill sshd, so the rollback was not exercised")
	}
	banner(t)
	if leftovers := d.sh(t, "ls -A /etc/ssh/sshd_config.d"); strings.TrimSpace(leftovers) != "90-tunnel.conf" {
		t.Fatalf("left in the drop-in directory: %q", leftovers)
	}

	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: sshdURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if out := d.sh(t, "ls -A /etc/ssh/sshd_config.d"); strings.TrimSpace(out) != "" {
		t.Fatalf("delete left %q", out)
	}
	banner(t)
}
