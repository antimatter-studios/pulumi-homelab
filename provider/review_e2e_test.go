package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// Tests for the points raised in the review of #98: imports reproduce the declared inputs exactly
// (zero diff), and deletes act only on the exact object they recorded.

type server interface {
	Read(p.ReadRequest) (p.ReadResponse, error)
	Diff(p.DiffRequest) (p.DiffResponse, error)
}

func contains2(sub string) func(string) bool {
	return func(c string) bool { return strings.Contains(c, sub) }
}

func reply(out string, code int) func(string) remotetest.Reply {
	return func(string) remotetest.Reply { return remotetest.Reply{Stdout: out, Code: code} }
}

// importAndDiff reads a resource by its id alone, as `pulumi import` does, and diffs the inputs
// the read returned against its state.
func importAndDiff(t *testing.T, s server, typ, id string) (p.ReadResponse, p.DiffResponse) {
	t.Helper()
	read, err := s.Read(p.ReadRequest{ID: id, Urn: urnOf(typ, "imported")})
	if err != nil || read.ID != id {
		t.Fatalf("import of %s: %+v %v", id, read, err)
	}
	diff, err := s.Diff(p.DiffRequest{ID: id, Urn: urnOf(typ, "imported"), State: read.Properties, Inputs: read.Inputs})
	if err != nil {
		t.Fatalf("diff after import of %s: %v", id, err)
	}
	if diff.HasChanges {
		t.Errorf("import of %s diffs as a change: %+v\ninputs %v\nstate %v", id, diff, read.Inputs, read.Properties)
	}
	return read, diff
}

const testKeyBody = "AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnopqrstuv"

func TestAuthorizedKeyImportFindsTheKeyByTheBodyInTheID(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("if [ -f"), reply("700\n600 chris\n#pulumi-homelab#\nssh-ed25519 AAAAother1234567890123456789 other\n"+
		`from="10.0.0.0/24,10.0.1.0/24",restrict ssh-ed25519 `+testKeyBody+" chris@laptop\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "AuthorizedKey", "/home/chris/.ssh/authorized_keys#"+testKeyBody)
	if got := read.Inputs.Get("key").AsString(); got != "ssh-ed25519 "+testKeyBody+" chris@laptop" {
		t.Errorf("key %q", got)
	}
	if got := read.Inputs.Get("options").AsArray(); got.Len() != 2 || got.Get(0).AsString() != `from="10.0.0.0/24,10.0.1.0/24"` {
		t.Errorf("options %v", got)
	}
	if read.Inputs.Get("user").AsString() != "chris" {
		t.Errorf("user %v", read.Inputs.Get("user"))
	}
}

func TestKernelCmdlineImportAndDiff(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("cmdline.txt"), reply("/boot/firmware/cmdline.txt\nconsole=tty1 root=PARTUUID=x rootwait\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "KernelCmdline", "/boot/firmware/cmdline.txt")
	// and a declared flag that is already there is no change either
	inputs := property.NewMap(map[string]property.Value{"flags": property.New([]property.Value{property.New("rootwait")})})
	if d, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urnOf("KernelCmdline", "c"), State: read.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Errorf("%+v %v", d, err)
	}
	inputs = property.NewMap(map[string]property.Value{"flags": property.New([]property.Value{property.New("cgroup_memory=1")})})
	if d, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urnOf("KernelCmdline", "c"), State: read.Properties, Inputs: inputs}); err != nil || !d.HasChanges {
		t.Errorf("a missing flag is a change: %+v %v", d, err)
	}
}

func TestManagedLineImportKeepsTheDeclaredText(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2(".bashrc"), reply("a\nexport PATH=\"$HOME/x:$PATH\" # pulumi-homelab:mise-shims\nb\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "ManagedLine", "/home/chris/.bashrc#mise-shims")
	if got := read.Inputs.Get("line").AsString(); got != `export PATH="$HOME/x:$PATH"` {
		t.Errorf("line %q", got)
	}
}

func TestPreconditionRefreshKeepsAnEmptyMessage(t *testing.T) {
	fake := remotetest.New().On("true", remotetest.Reply{})
	s := configured(t, fake)
	state := property.NewMap(map[string]property.Value{"check": property.New("true"), "message": property.New(""), "root": property.New(false)})
	read, err := s.Read(p.ReadRequest{ID: "true", Urn: urnOf("Precondition", "p"), Properties: state, Inputs: state})
	if err != nil || read.Properties.Get("message").AsString() != "" {
		t.Fatalf("%v %v", read.Properties, err)
	}
	// an import, with nothing remembered, still falls back to the check
	imported, _ := importAndDiff(t, s, "Precondition", "true")
	if imported.Properties.Get("message").AsString() != "true" {
		t.Errorf("%v", imported.Properties)
	}
}

func sshKeyFake(publicKey, fingerprint string) *remotetest.Runner {
	fake := remotetest.New()
	fake.When(contains2("ssh-keygen -lf"), reply(publicKey+"\n"+fingerprintMarker+"\n256 "+fingerprint+" c (X)\n"+statMarker+"\n600|svc|svc\n644|svc|svc\n", 0), 0)
	return fake
}

func TestSshKeyImportLabelsAnRSAKeyAsRSA(t *testing.T) {
	s := configured(t, sshKeyFake("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQ backup key", "SHA256:rsa"))
	read, _ := importAndDiff(t, s, "SshKey", "/home/svc/.ssh/id_rsa")
	if read.Inputs.Get("type").AsString() != "rsa" || read.Inputs.Get("comment").AsString() != "backup key" {
		t.Errorf("%v", read.Inputs)
	}
	// declaring the real type is no replacement
	inputs := property.NewMap(map[string]property.Value{
		"path": property.New("/home/svc/.ssh/id_rsa"), "type": property.New("rsa"), "owner": property.New("svc"), "group": property.New("svc"),
	})
	if d, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urnOf("SshKey", "k"), State: read.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Errorf("%+v %v", d, err)
	}
}

func TestSudoRuleImportRecoversTheRule(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("sudoers.d/backup"), reply("440 root\n# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n"+
		"%backup ALL=(root) NOPASSWD: /usr/bin/borg, /usr/bin/systemctl restart borg\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "SudoRule", "backup")
	if read.Inputs.Get("group").AsString() != "backup" || !read.Inputs.Get("passwordless").AsBool() || read.Inputs.Get("commands").AsArray().Len() != 2 {
		t.Errorf("%v", read.Inputs)
	}
}

func TestDeletesUseTheDirectoryInStateEvenWhenEmpty(t *testing.T) {
	fake := remotetest.New()
	fake.When(func(string) bool { return true }, reply("", 0), 0)
	s := configured(t, fake)
	sudo := property.NewMap(map[string]property.Value{"file": property.New("admin"), "directory": property.New(""), "content": property.New("x"), "mode": property.New("0440"), "owner": property.New("root")})
	if err := s.Delete(p.DeleteRequest{ID: "admin", Urn: urnOf("SudoRule", "admin"), Properties: sudo}); err != nil {
		t.Fatal(err)
	}
	unit := property.NewMap(map[string]property.Value{"name": property.New("x"), "unit": property.New("u"), "enabled": property.New(true), "started": property.New(true),
		"mode": property.New("0644"), "unitFileState": property.New("enabled"), "directory": property.New("")})
	if err := s.Delete(p.DeleteRequest{ID: "x", Urn: urnOf("SystemdUnit", "x"), Properties: unit}); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(fake.Commands(), "\n")
	if !strings.Contains(commands, `rm -f '\''/admin'\''`) || !strings.Contains(commands, `rm -f '\''/x.service'\''`) || strings.Contains(commands, "/etc/") {
		t.Fatalf("%s", commands)
	}
}

func TestSystemdInstanceImportNamesTheInstanceAndItsSuffix(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("systemctl cat '\\''garage-offsite@.timer'\\''"), reply("enabled\n#pulumi-homelab#\nactive\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "SystemdInstance", `garage-offsite@hourly\x2dx.timer`)
	if read.Inputs.Get("instance").AsString() != "hourly-x" || read.Inputs.Get("suffix").AsString() != "timer" || read.Inputs.Get("template").AsString() != "garage-offsite" {
		t.Errorf("%v", read.Inputs)
	}
}

// A change to the route alone (another address, port or jump) is no change to the unit: the diff
// reads nothing about the route, so a provider reconfigured for a new route shows zero changes.
func TestSystemdUnitRouteOnlyChangeIsZeroDiff(t *testing.T) {
	inputs := property.NewMap(map[string]property.Value{"name": property.New("k3s"), "unit": property.New("[Unit]\n")})
	state := property.NewMap(map[string]property.Value{"name": property.New("k3s"), "unit": property.New("[Unit]\n"), "enabled": property.New(true), "started": property.New(true),
		"mode": property.New("0644"), "unitFileState": property.New("enabled"), "directory": property.New("/etc/systemd/system")})
	for _, cfg := range []map[string]property.Value{homelabServerConfig(), with(with(homelabServerConfig(), "address", property.New("192.168.0.47")), "port", property.New(22.0))} {
		s := newServer(t, (&capture{runner: remotetest.New()}).factory)
		if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(cfg)}); err != nil {
			t.Fatal(err)
		}
		if d, err := s.Diff(p.DiffRequest{ID: "k3s", Urn: urnOf("SystemdUnit", "k3s"), State: state, Inputs: inputs}); err != nil || d.HasChanges {
			t.Fatalf("%v: %+v %v", cfg["address"], d, err)
		}
	}
}
