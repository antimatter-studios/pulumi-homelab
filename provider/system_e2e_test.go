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
	fake.On(`sudo -n sh -c 'userdel '\''admin'\'' || true'`, remotetest.Reply{})
	state := property.NewMap(map[string]property.Value{
		"name": property.New("admin"), "shell": property.New("/bin/bash"), "home": property.New("/home/admin"),
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
