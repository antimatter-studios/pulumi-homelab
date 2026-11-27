//go:build integration

package homelab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// Integration tests of the system resources against a real Debian 13 (the transport's sshd image,
// which has sudo, visudo and ssh-keygen). Commands run as the unprivileged user chris with
// passwordless sudo, exactly as the provider sends them; the ssh hop itself is covered by
// provider/transport's own integration test. Run with: go test -tags integration ./...
const systemImage = "pulumi-homelab-transport-sshd:debian13"

type dockerRunner struct{ id string }

func (d dockerRunner) Run(ctx context.Context, command string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", "-u", "chris", d.id, "sh", "-c", command)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

func debian(t *testing.T) (dockerRunner, func(string) string) {
	t.Helper()
	build := exec.Command("docker", "build", "-q", "-t", systemImage, "transport/testdata/debian13-sshd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", systemImage, "sleep", "infinity").Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	root := func(command string) string {
		out, err := exec.Command("docker", "exec", id, "sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", command, err, out)
		}
		return string(out)
	}
	return dockerRunner{id}, root
}

type lifecycleServer interface {
	Create(p.CreateRequest) (p.CreateResponse, error)
	Read(p.ReadRequest) (p.ReadResponse, error)
	Update(p.UpdateRequest) (p.UpdateResponse, error)
	Diff(p.DiffRequest) (p.DiffResponse, error)
	Delete(p.DeleteRequest) error
}

func serve(t *testing.T, runner dockerRunner) lifecycleServer {
	s := newServer(t, (&capture{runner: remote.Runner(runner)}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	return s
}

func onDebian(t *testing.T) (lifecycleServer, func(string) string) {
	runner, root := debian(t)
	return serve(t, runner), root
}

// cycle is a resource's whole life against a real machine: create, refresh (no changes), an
// update to other inputs, refresh (no changes), delete. check runs after each stage with its
// name, for the test to look at the machine.
func cycle(t *testing.T, s lifecycleServer, typ string, create, update property.Map, check func(stage string, state property.Map)) {
	t.Helper()
	urn := urnOf(typ, "it")
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: create})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	check("create", created.Properties)
	refreshed := refreshNoChanges(t, s, urn, created.ID, created.Properties, create, "after create")
	if d, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: refreshed, Inputs: update}); err != nil || !d.HasChanges || len(replacesOf(d)) > 0 {
		t.Fatalf("the update is not an in-place change: %+v %v", d, err)
	}
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: urn, State: refreshed, Inputs: update})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	check("update", updated.Properties)
	refreshed = refreshNoChanges(t, s, urn, created.ID, updated.Properties, update, "after update")
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: refreshed}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	check("delete", refreshed)
}

func refreshNoChanges(t *testing.T, s lifecycleServer, urn presource.URN, id string, state, inputs property.Map, when string) property.Map {
	t.Helper()
	read, err := s.Read(p.ReadRequest{ID: id, Urn: urn, Properties: state, Inputs: inputs})
	if err != nil || read.ID != id {
		t.Fatalf("refresh %s: %+v %v", when, read, err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: id, Urn: urn, State: read.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("refresh %s shows changes: %+v %v\nstate %v", when, d, err, read.Properties)
	}
	return read.Properties
}

func replacesOf(d p.DiffResponse) []string {
	var out []string
	for k, v := range d.DetailedDiff {
		if v.Kind == p.UpdateReplace || v.Kind == p.AddReplace || v.Kind == p.DeleteReplace {
			out = append(out, k)
		}
	}
	return out
}

func m(kv ...any) property.Map {
	out := map[string]property.Value{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = toValue(kv[i+1])
	}
	return property.NewMap(out)
}

func toValue(v any) property.Value {
	switch x := v.(type) {
	case []string:
		vs := make([]property.Value, len(x))
		for i, e := range x {
			vs[i] = property.New(e)
		}
		return property.New(vs)
	case int:
		return property.New(float64(x))
	case property.Map:
		return property.New(x)
	case map[string]string:
		vs := map[string]property.Value{}
		for k, e := range x {
			vs[k] = property.New(e)
		}
		return property.New(vs)
	case string:
		return property.New(x)
	case bool:
		return property.New(x)
	case float64:
		return property.New(x)
	}
	panic(fmt.Sprintf("%T", v))
}

// The home directory, and what is in it, survives the User's delete.
func TestUserHomeSurvivesDeleteOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	urn := urnOf("User", "svc")
	inputs := property.NewMap(map[string]property.Value{"name": property.New("svc"), "shell": property.New("/usr/sbin/nologin"), "uid": property.New(995.0)})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if created.Properties.Get("uid").AsNumber() != 995 || created.Properties.Get("home").AsString() != "/home/svc" {
		t.Fatalf("%v", created.Properties)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "svc", Urn: urn, State: created.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("diff after create %+v %v", d, err)
	}
	root("echo world > /home/svc/data && chown svc /home/svc/data")
	if err := s.Delete(p.DeleteRequest{ID: "svc", Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if out := root("getent passwd svc || echo gone"); strings.TrimSpace(out) != "gone" {
		t.Fatalf("account still there: %s", out)
	}
	if out := root("cat /home/svc/data"); out != "world\n" {
		t.Fatalf("home lost: %q", out)
	}
}

// The rule is validated by the real visudo, installed 0440 root, and removed by delete; a rule
// that does not parse is refused and nothing is installed.
func TestSudoRuleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	urn := urnOf("SudoRule", "backup")
	inputs := property.NewMap(map[string]property.Value{"user": property.New("chris"), "passwordless": property.New(true),
		"commands": property.New([]property.Value{property.New("/usr/bin/true")}), "file": property.New("backup"), "directory": property.New("/etc/sudoers.d")})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if out := root("stat -c '%a %U' /etc/sudoers.d/backup"); out != "440 root\n" {
		t.Fatalf("%q", out)
	}
	imported, err := s.Read(p.ReadRequest{ID: "backup", Urn: urn})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "backup", Urn: urn, State: imported.Properties, Inputs: imported.Inputs}); err != nil || d.HasChanges {
		t.Fatalf("import diff %+v %v", d, err)
	}
	bad := property.NewMap(map[string]property.Value{"user": property.New("chris"), "passwordless": property.New(true),
		"commands": property.New([]property.Value{property.New("relative/path (")}), "file": property.New("broken"), "directory": property.New("/etc/sudoers.d")})
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("SudoRule", "broken"), Properties: bad}); err == nil || !strings.Contains(err.Error(), "did not parse") {
		t.Fatalf("got %v", err)
	}
	if out := root("ls /etc/sudoers.d"); strings.Contains(out, "broken") {
		t.Fatal("installed a rule that did not parse")
	}
	if err := s.Delete(p.DeleteRequest{ID: "backup", Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if out := root("ls /etc/sudoers.d"); strings.Contains(out, "backup") || !strings.Contains(out, "chris") {
		t.Fatalf("%q", out)
	}
}

// The key goes in with 0700/0600 and the owner, an import diffs as no change, and delete leaves
// the other key in the file.
func TestAuthorizedKeyOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnopqrstuv laptop"
	root("echo 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIotherotherotherother other' > /home/chris/.ssh/authorized_keys && chown chris /home/chris/.ssh/authorized_keys")
	before := root("cat /home/chris/.ssh/authorized_keys")
	urn := urnOf("AuthorizedKey", "laptop")
	inputs := property.NewMap(map[string]property.Value{"user": property.New("chris"), "key": property.New(key),
		"options": property.New([]property.Value{property.New("restrict")})})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if created.Properties.Get("mode").AsString() != "0600" || created.Properties.Get("owner").AsString() != "chris" {
		t.Fatalf("%v", created.Properties)
	}
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: urn})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: imported.Properties, Inputs: imported.Inputs}); err != nil || d.HasChanges {
		t.Fatalf("import diff %+v %v", d, err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if after := root("cat /home/chris/.ssh/authorized_keys"); after != before {
		t.Fatalf("delete changed the other keys:\n%q\n%q", before, after)
	}
}

// Generated with the declared owner and modes; an import labels the type; delete without
// allowDelete leaves both halves.
func TestSshKeyOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	urn := urnOf("SshKey", "deploy")
	inputs := property.NewMap(map[string]property.Value{"path": property.New("/home/chris/.ssh/id_rsa"), "type": property.New("rsa"), "bits": property.New(2048.0),
		"comment": property.New("deploy"), "owner": property.New("chris"), "group": property.New("chris")})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if out := root("stat -c '%a %U %G' /home/chris/.ssh/id_rsa /home/chris/.ssh/id_rsa.pub"); out != "600 chris chris\n644 chris chris\n" {
		t.Fatalf("%q", out)
	}
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: urn})
	if err != nil || imported.Inputs.Get("type").AsString() != "rsa" || imported.Inputs.Get("comment").AsString() != "deploy" {
		t.Fatalf("%v %v", imported.Inputs, err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err == nil {
		t.Fatal("deleted without allowDelete")
	}
	root("test -f /home/chris/.ssh/id_rsa && test -f /home/chris/.ssh/id_rsa.pub")
}

// The line goes before its anchor, an import diffs as no change, and delete takes out only it.
func TestManagedLineOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	before := root("cat /home/chris/.bashrc")
	urn := urnOf("ManagedLine", "shims")
	inputs := property.NewMap(map[string]property.Value{"path": property.New("/home/chris/.bashrc"), "line": property.New(`export PATH="$HOME/x:$PATH"`),
		"marker": property.New("shims"), "position": property.New("before"), "anchor": property.New("case $- in")})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if !created.Properties.Get("placed").AsBool() {
		t.Fatalf("%v", created.Properties)
	}
	if d, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: created.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("diff %+v %v", d, err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if after := root("cat /home/chris/.bashrc"); after != before {
		t.Fatal("delete did not restore the file")
	}
}

// The entry goes in with its marker, an import diffs as no change, and delete takes out only it.
func TestFstabEntryOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("printf '# static\\nPARTUUID=1-02 / ext4 defaults 0 1\\n' > /etc/fstab.test")
	before := root("cat /etc/fstab.test")
	urn := urnOf("FstabEntry", "data")
	inputs := property.NewMap(map[string]property.Value{"source": property.New("UUID=abcd"), "target": property.New("/mnt/data"), "type": property.New("ext4"),
		"options": property.New([]property.Value{property.New("defaults"), property.New("nofail")}), "file": property.New("/etc/fstab.test")})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if out := root("cat /etc/fstab.test"); out != before+"# pulumi-homelab\nUUID=abcd /mnt/data ext4 defaults,nofail 0 0\n" {
		t.Fatalf("%q", out)
	}
	if d, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: created.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("%+v %v", d, err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if out := root("cat /etc/fstab.test"); out != before {
		t.Fatalf("%q", out)
	}
}

// Lines go into their section, an unknown overlay is refused, and delete restores the file.
func TestBootConfigOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("mkdir -p /boot/firmware/overlays && touch /boot/firmware/overlays/dwc2.dtbo && " +
		"printf 'dtparam=audio=on\\n\\n[pi5]\\n# fast\\narm_freq=2400\\n' > /boot/firmware/config.txt")
	before := root("cat /boot/firmware/config.txt")
	urn := urnOf("BootConfig", "usb")
	inputs := property.NewMap(map[string]property.Value{"section": property.New("pi5"), "settings": property.New(map[string]property.Value{"arm_boost": property.New(true)}),
		"overlays": property.New([]property.Value{property.New("dtoverlay=dwc2,dr_mode=host")})})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	// the blank line is the TypeScript's: a section's body keeps the file's trailing empty line, and
	// new lines go after it
	if out := root("cat /boot/firmware/config.txt"); out != "dtparam=audio=on\n\n[pi5]\n# fast\narm_freq=2400\n\narm_boost=1\ndtoverlay=dwc2,dr_mode=host\n" {
		t.Fatalf("%q", out)
	}
	bad := property.NewMap(map[string]property.Value{"section": property.New("pi5"), "overlays": property.New([]property.Value{property.New("dtoverlay=dwc3")})})
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("BootConfig", "typo"), Properties: bad}); err == nil || !strings.Contains(err.Error(), "no overlay named dwc3") {
		t.Fatalf("%v", err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	// every line this resource added is gone and every other line is as it was; the blank line the
	// write left stays, as it does under the TypeScript
	if out := root("cat /boot/firmware/config.txt"); strings.TrimRight(out, "\n") != strings.TrimRight(before, "\n") {
		t.Fatalf("%q", out)
	}
}

// The real sshd validates the drop-in, which is written in the production order; an import diffs
// as no change; delete removes only the drop-in.
func TestSshdConfigOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("ssh-keygen -A >/dev/null")
	urn := urnOf("SshdConfig", "tunnel")
	inputs := property.NewMap(map[string]property.Value{"file": property.New("90-tunnel"), "directory": property.New(sshdDirectory), "config": property.New(sshdConfig),
		"settings":  property.New(map[string]property.Value{"x11Forwarding": property.New(false), "allowTcpForwarding": property.New("remote"), "clientAliveInterval": property.New(30.0)}),
		"unchecked": property.New(map[string]property.Value{"PermitTTY": property.New("no")}),
		"match":     property.New(map[string]property.Value{"User": property.New("tunnel")})})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	want := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nMatch User tunnel\n    AllowTcpForwarding remote\n    ClientAliveInterval 30\n    X11Forwarding no\n    PermitTTY no\n"
	if out := root("cat /etc/ssh/sshd_config.d/90-tunnel.conf"); out != want {
		t.Fatalf("%q", out)
	}
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: urn})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: imported.Properties, Inputs: imported.Inputs}); err != nil || d.HasChanges {
		t.Fatalf("%+v %v", d, err)
	}
	bad := property.NewMap(map[string]property.Value{"file": property.New("91-bad"), "directory": property.New(sshdDirectory), "config": property.New(sshdConfig),
		"unchecked": property.New(map[string]property.Value{"NoSuchKeyword": property.New("x")})})
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("SshdConfig", "bad"), Properties: bad}); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Fatalf("%v", err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if out := root("ls /etc/ssh/sshd_config.d"); strings.Contains(out, "90-tunnel") || strings.Contains(out, "91-bad") {
		t.Fatalf("%q", out)
	}
}
