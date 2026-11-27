package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

func configured(t *testing.T, fake *remotetest.Runner) interface {
	Create(p.CreateRequest) (p.CreateResponse, error)
	Read(p.ReadRequest) (p.ReadResponse, error)
	Update(p.UpdateRequest) (p.UpdateResponse, error)
	Diff(p.DiffRequest) (p.DiffResponse, error)
	Delete(p.DeleteRequest) error
} {
	t.Helper()
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	return s
}

func groupInputs(gid float64, renumber bool) property.Map {
	return property.NewMap(map[string]property.Value{
		"name": property.New("storage"), "gid": property.New(gid), "renumber": property.New(renumber),
	})
}

// Create, read, update, diff and delete through the gRPC surface with the fake runner.
func TestGroupLifecycle(t *testing.T) {
	gid := "1002"
	fake := remotetest.New()
	exists := false
	fake.When(func(c string) bool { return c == "getent group 'storage'" }, func(string) remotetest.Reply {
		if !exists {
			return remotetest.Reply{Code: 2}
		}
		return remotetest.Reply{Stdout: "storage:x:" + gid + ":admin,player\n"}
	}, 0)
	fake.When(func(c string) bool { return c == `sudo -n sh -c 'groupadd -g 1002 '\''storage'\'''` }, func(string) remotetest.Reply {
		exists = true
		return remotetest.Reply{}
	}, 0)
	fake.When(func(c string) bool { return c == `sudo -n sh -c 'groupmod -g 1003 '\''storage'\'''` }, func(string) remotetest.Reply {
		gid = "1003"
		return remotetest.Reply{}
	}, 0)
	s := configured(t, fake)

	// create
	created, err := s.Create(p.CreateRequest{Urn: groupURN, Properties: groupInputs(1002, false)})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "storage" || created.Properties.Get("gid").AsNumber() != 1002 ||
		created.Properties.Get("members").AsArray().Len() != 2 {
		t.Fatalf("created %s %v", created.ID, created.Properties)
	}

	// read: no drift
	read, err := s.Read(p.ReadRequest{ID: "storage", Urn: groupURN, Properties: created.Properties, Inputs: groupInputs(1002, false)})
	if err != nil || read.ID != "storage" || read.Properties.Get("gid").AsNumber() != 1002 {
		t.Fatalf("read %v %v", read, err)
	}

	// diff: same gid is no change; another gid is drift, an update not a replace
	same, err := s.Diff(p.DiffRequest{ID: "storage", Urn: groupURN, State: created.Properties, Inputs: groupInputs(1002, false)})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged diff %+v %v", same, err)
	}
	moved, err := s.Diff(p.DiffRequest{ID: "storage", Urn: groupURN, State: created.Properties, Inputs: groupInputs(1003, false)})
	if err != nil || !moved.HasChanges || moved.DetailedDiff["gid"].Kind != p.Update {
		t.Fatalf("gid diff %+v %v", moved, err)
	}
	renamed := property.NewMap(map[string]property.Value{"name": property.New("archive"), "gid": property.New(1002.0)})
	rn, err := s.Diff(p.DiffRequest{ID: "storage", Urn: groupURN, State: created.Properties, Inputs: renamed})
	if err != nil || !rn.HasChanges || !rn.DeleteBeforeReplace || rn.DetailedDiff["name"].Kind != p.UpdateReplace {
		t.Fatalf("rename diff %+v %v", rn, err)
	}

	// update without renumber is refused with the message, and runs no groupmod
	_, err = s.Update(p.UpdateRequest{ID: "storage", Urn: groupURN, State: created.Properties, Inputs: groupInputs(1003, false)})
	if err == nil || !strings.Contains(err.Error(), "exists with gid 1002 and the code says 1003") {
		t.Fatalf("update without renumber: %v", err)
	}
	if fake.Ran("groupmod") {
		t.Fatal("groupmod ran without renumber")
	}

	// update with renumber renumbers
	updated, err := s.Update(p.UpdateRequest{ID: "storage", Urn: groupURN, State: created.Properties, Inputs: groupInputs(1003, true)})
	if err != nil || updated.Properties.Get("gid").AsNumber() != 1003 || !updated.Properties.Get("renumber").AsBool() {
		t.Fatalf("update %v %v", updated, err)
	}

	// read after the group vanished: forgotten
	exists = false
	gone, err := s.Read(p.ReadRequest{ID: "storage", Urn: groupURN, Properties: updated.Properties})
	if err != nil || gone.ID != "" {
		t.Fatalf("read of a vanished group %+v %v", gone, err)
	}

	// delete is a no-op: no command reaches the machine
	before := len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: "storage", Urn: groupURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Commands()) != before {
		t.Fatalf("delete ran %v", fake.Commands()[before:])
	}
}

// A failing groupadd surfaces as the must error; sudo-password sends the password on stdin.
func TestGroupCreateFailureAndSudoPassword(t *testing.T) {
	fake := remotetest.New().
		On("getent group 'storage'", remotetest.Reply{Code: 2}).
		On(`sudo -S -p '' sh -c 'groupadd -g 1002 '\''storage'\'''`, remotetest.Reply{Code: 9, Stderr: "groupadd: GID '1002' already exists\n"})
	s := newServer(t, (&capture{runner: fake}).factory)
	cfg := with(with(homelabServerConfig(), "escalation", property.New("sudo-password")), "sudoPassword", property.New("pw").WithSecret(true))
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(cfg)}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create(p.CreateRequest{Urn: groupURN, Properties: groupInputs(1002, false)})
	want := "`sudo -S -p '' sh -c 'groupadd -g 1002 '\\''storage'\\'''` failed on chris@127.0.0.1 (exit 9): groupadd: GID '1002' already exists"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
	for _, c := range fake.Calls {
		if c.Stdin != "pw\n" {
			t.Fatalf("%q got stdin %q", c.Command, c.Stdin)
		}
	}
}
