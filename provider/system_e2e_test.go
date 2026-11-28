package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

func urnOf(typ, name string) presource.URN {
	return presource.URN("urn:pulumi:s::p::homelab:index:" + typ + "::" + name)
}

// A Precondition that holds is created with the check as its id; one that fails reports the
// message; a refresh that finds it no longer true forgets it; delete sends nothing.
func TestPreconditionLifecycle(t *testing.T) {
	holds := true
	fake := remotetest.New()
	fake.When(func(c string) bool { return c == `sudo -n sh -c 'grep -q cgroup_memory /proc/cmdline'` }, func(string) remotetest.Reply {
		if holds {
			return remotetest.Reply{}
		}
		return remotetest.Reply{Code: 1}
	}, 0)
	s := configured(t, fake)
	urn := urnOf("Precondition", "cgroups")
	inputs := property.NewMap(map[string]property.Value{
		"check": property.New("grep -q cgroup_memory /proc/cmdline"), "message": property.New("run `sudo reboot` and deploy again"), "root": property.New(true),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil || created.ID != "grep -q cgroup_memory /proc/cmdline" {
		t.Fatalf("create %v %v", created, err)
	}
	holds = false
	gone, err := s.Read(p.ReadRequest{ID: created.ID, Urn: urn, Properties: created.Properties})
	if err != nil || gone.ID != "" {
		t.Fatalf("read %+v %v", gone, err)
	}
	if _, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs}); err == nil || err.Error() != "run `sudo reboot` and deploy again" {
		t.Fatalf("failing create: %v", err)
	}
	before := len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil || len(fake.Commands()) != before {
		t.Fatalf("delete %v", err)
	}
}

// SudoRule's file defaults to the resource's name at check, and a name sudo would ignore fails
// there, before anything reaches the machine.
func TestSudoRuleCheckDefaultsTheFileToTheName(t *testing.T) {
	s := newServer(t, (&capture{runner: remotetest.New()}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := property.NewMap(map[string]property.Value{"user": property.New("admin"), "passwordless": property.New(true)})
	checked, err := s.Check(p.CheckRequest{Urn: urnOf("SudoRule", "admin-nopasswd"), Inputs: inputs})
	if err != nil || len(checked.Failures) != 0 || checked.Inputs.Get("file").AsString() != "admin-nopasswd" ||
		checked.Inputs.Get("directory").AsString() != "/etc/sudoers.d" {
		t.Fatalf("check %+v %v", checked, err)
	}
	bad, err := s.Check(p.CheckRequest{Urn: urnOf("SudoRule", "admin.conf"), Inputs: inputs})
	if err != nil || len(bad.Failures) != 1 || !strings.Contains(bad.Failures[0].Reason, "would never apply") {
		t.Fatalf("check of a dotted name %+v %v", bad, err)
	}
}

// User refuses to drop memberships before running anything that changes the machine.
func TestUserRefusesToDropSudo(t *testing.T) {
	fake := remotetest.New().
		On("getent passwd 'admin' && id -nG 'admin' && id -gn 'admin'", remotetest.Reply{Stdout: "admin:x:1000:1000::/home/admin:/bin/bash\nadmin sudo audio\nadmin\n"})
	s := configured(t, fake)
	inputs := property.NewMap(map[string]property.Value{
		"name": property.New("admin"), "shell": property.New("/bin/bash"), "groups": property.New([]property.Value{property.New("audio")}),
	})
	_, err := s.Create(p.CreateRequest{Urn: urnOf("User", "admin"), Properties: inputs})
	if err == nil || !strings.Contains(err.Error(), "remove 1 membership, including sudo") {
		t.Fatalf("got %v", err)
	}
	if fake.Ran("usermod") || fake.Ran("useradd") {
		t.Fatal("changed the account anyway")
	}
	// delete never removes the home
	fake.On("getent -s files passwd -- 'admin'", remotetest.Reply{Stdout: "admin:x:1000:1000::/home/admin:/bin/bash\n"})
	fake.When(contains2("userdel --"), reply("", 0), 1)
	state := property.NewMap(map[string]property.Value{
		"name": property.New("admin"), "uid": property.New(1000.0), "shell": property.New("/bin/bash"), "home": property.New("/home/admin"),
		"createHome": property.New(true), "groups": property.New([]property.Value{}), "allowGroupRemoval": property.New(false),
	})
	if err := s.Delete(p.DeleteRequest{ID: "admin", Urn: urnOf("User", "admin"), Properties: state}); err != nil {
		t.Fatal(err)
	}
}

// SshKey refuses to delete unless allowDelete is set, and sends nothing when it refuses.
func TestSshKeyDeleteRefusesByDefault(t *testing.T) {
	fake := remotetest.New()
	s := configured(t, fake)
	state := property.NewMap(map[string]property.Value{
		"path": property.New("/k/id"), "type": property.New("ed25519"), "comment": property.New("k"), "owner": property.New("root"),
		"group": property.New("root"), "privateMode": property.New("0600"), "publicMode": property.New("0644"),
		"allowDelete": property.New(false), "publicKey": property.New("ssh-ed25519 AAAA k"), "fingerprint": property.New("SHA256:x"),
	})
	if err := s.Delete(p.DeleteRequest{ID: "/k/id", Urn: urnOf("SshKey", "k"), Properties: state}); err == nil || !strings.Contains(err.Error(), "refusing to delete") {
		t.Fatalf("got %v", err)
	}
	if len(fake.Commands()) != 0 {
		t.Fatalf("ran %v", fake.Commands())
	}
}

// The #38 switch imports root-only checks (homelab-server's check-flux-source and
// check-flux-applied) with an ID ending "@root": the check runs escalated, and the stored ID is the
// check itself. Without the suffix it runs unescalated, as the TypeScript's import would.
func TestPreconditionImportWithRoot(t *testing.T) {
	const check = "test -r /root/flux"
	fake := remotetest.New()
	fake.When(func(c string) bool { return c == `sudo -n sh -c 'test -r /root/flux'` }, func(string) remotetest.Reply { return remotetest.Reply{} }, 0)
	fake.When(func(c string) bool { return c == check }, func(string) remotetest.Reply { return remotetest.Reply{Code: 1} }, 0)
	s := configured(t, fake)
	urn := urnOf("Precondition", "check-flux-source")

	imported, err := s.Read(p.ReadRequest{ID: check + "@root", Urn: urn})
	if err != nil || imported.ID != check || !imported.Properties.Get("root").AsBool() {
		t.Fatalf("import with @root: %+v %v", imported, err)
	}
	declared := property.NewMap(map[string]property.Value{"check": property.New(check), "message": property.New(check), "root": property.New(true)})
	if d, err := s.Diff(p.DiffRequest{ID: imported.ID, Urn: urn, State: imported.Properties, Inputs: declared}); err != nil || d.HasChanges {
		t.Fatalf("import diff %+v %v", d, err)
	}
	if gone, err := s.Read(p.ReadRequest{ID: check, Urn: urn}); err != nil || gone.ID != "" {
		t.Fatalf("import without @root should run unescalated and find it missing: %+v %v", gone, err)
	}
}

// homelab-server declares avahi-homelab.local and timer-agent-quota-5min disabled and stopped. An
// import must find them (the template exists), where a refresh forgets such an instance as the
// TypeScript does.
func TestSystemdInstanceImportKeepsDisabledStopped(t *testing.T) {
	fake := remotetest.New()
	fake.When(func(c string) bool { return strings.Contains(c, "systemctl cat") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: "disabled\n#pulumi-homelab#\ninactive\n"}
	}, 0)
	s := configured(t, fake)
	urn := urnOf("SystemdInstance", "avahi")
	imported, err := s.Read(p.ReadRequest{ID: "avahi-alias@homelab.local.service", Urn: urn})
	if err != nil || imported.ID == "" || imported.Properties.Get("enabled").AsBool() || imported.Properties.Get("started").AsBool() {
		t.Fatalf("import of a disabled, stopped instance: %+v %v", imported, err)
	}
	if gone, err := s.Read(p.ReadRequest{ID: imported.ID, Urn: urn, Properties: imported.Properties}); err != nil || gone.ID != "" {
		t.Fatalf("a refresh should forget a disabled, stopped instance: %+v %v", gone, err)
	}
}
