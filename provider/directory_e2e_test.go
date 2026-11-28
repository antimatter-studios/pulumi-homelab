package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var directoryURN = presource.URN("urn:pulumi:s::p::homelab:index:Directory::shared")

const (
	sharedRead   = `sudo -n sh -c '{ test -e '\''/srv/shared'\'' || test -L '\''/srv/shared'\''; } || exit 9; stat -c '\''%F|%a|%U|%G'\'' '\''/srv/shared'\'''`
	sharedApply  = `sudo -n sh -c 'mkdir -p '\''/srv/shared'\'' && chmod 2775 '\''/srv/shared'\'' && chown '\''root:staff'\'' '\''/srv/shared'\'''`
	sharedSticky = `sudo -n sh -c 'mkdir -p '\''/srv/shared'\'' && chmod 3775 '\''/srv/shared'\'' && chown '\''root:staff'\'' '\''/srv/shared'\'''`
	sharedRmdir  = `sudo -n sh -c 'rmdir '\''/srv/shared'\'''`
)

func directoryInputs(extra map[string]property.Value) property.Map {
	m := map[string]property.Value{
		"path": property.New("/srv/shared"), "mode": property.New("0775"), "group": property.New("staff"), "setgid": property.New(true),
	}
	for k, v := range extra {
		m[k] = v
	}
	return property.NewMap(m)
}

// Create, read, update, diff and delete through the gRPC surface with the fake runner.
func TestDirectoryLifecycle(t *testing.T) {
	fake := remotetest.New()
	statOut := ""
	fake.When(func(c string) bool { return c == sharedRead }, func(string) remotetest.Reply {
		if statOut == "" {
			return remotetest.Reply{Code: 9}
		}
		return remotetest.Reply{Stdout: statOut}
	}, 0)
	fake.When(func(c string) bool { return c == sharedApply }, func(string) remotetest.Reply {
		statOut = "directory|2775|root|staff\n"
		return remotetest.Reply{}
	}, 0)
	fake.When(func(c string) bool { return c == sharedSticky }, func(string) remotetest.Reply {
		statOut = "directory|3775|root|staff\n"
		return remotetest.Reply{}
	}, 0)
	fake.On(sharedRmdir, remotetest.Reply{})
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}

	// check fills the defaults the TypeScript constructor did, and refuses a bad mode at preview
	checked, err := s.Check(p.CheckRequest{Urn: directoryURN, Inputs: directoryInputs(nil)})
	if err != nil || len(checked.Failures) != 0 || checked.Inputs.Get("owner").AsString() != "root" {
		t.Fatalf("check %+v %v", checked, err)
	}
	bad, err := s.Check(p.CheckRequest{Urn: directoryURN, Inputs: directoryInputs(map[string]property.Value{"mode": property.New("2775")})})
	if err != nil || len(bad.Failures) != 1 || bad.Failures[0].Property != "mode" || !strings.Contains(bad.Failures[0].Reason, "one or the other") {
		t.Fatalf("bad check %+v %v", bad, err)
	}
	notMode, err := s.Check(p.CheckRequest{Urn: directoryURN, Inputs: property.NewMap(map[string]property.Value{
		"path": property.New("/srv/shared"), "mode": property.New("u+rwx"),
	})})
	if err != nil || len(notMode.Failures) != 1 || notMode.Failures[0].Reason != "u+rwx is not a mode: write three or four octal digits, as in '0755' or '2775'" {
		t.Fatalf("not-a-mode check %+v %v", notMode, err)
	}

	// create: the flags are folded into one mode, written once
	created, err := s.Create(p.CreateRequest{Urn: directoryURN, Properties: checked.Inputs})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "/srv/shared" || created.Properties.Get("mode").AsString() != "2775" || created.Properties.Get("owner").AsString() != "root" {
		t.Fatalf("created %s %v", created.ID, created.Properties)
	}

	// read: stat's answer is the declared mode, so no drift
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: directoryURN, Properties: created.Properties, Inputs: checked.Inputs})
	if err != nil || read.Properties.Get("mode").AsString() != "2775" || read.Properties.Get("group").AsString() != "staff" {
		t.Fatalf("read %+v %v", read, err)
	}

	// import: the inputs are what the machine says
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: directoryURN})
	if err != nil || imported.Inputs.Get("path").AsString() != "/srv/shared" || imported.Inputs.Get("mode").AsString() != "2775" {
		t.Fatalf("import %+v %v", imported, err)
	}

	// diff
	same, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: directoryURN, State: created.Properties, Inputs: checked.Inputs})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged diff %+v %v", same, err)
	}
	sticky := directoryInputs(map[string]property.Value{"sticky": property.New(true)})
	sd, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: directoryURN, State: created.Properties, Inputs: sticky})
	if err != nil || !sd.HasChanges || sd.DetailedDiff["mode"].Kind != p.Update {
		t.Fatalf("mode diff %+v %v", sd, err)
	}
	moved := directoryInputs(map[string]property.Value{"path": property.New("/srv/elsewhere")})
	md, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: directoryURN, State: created.Properties, Inputs: moved})
	if err != nil || !md.HasChanges || md.DeleteBeforeReplace || md.DetailedDiff["path"].Kind != p.UpdateReplace {
		t.Fatalf("path diff %+v %v", md, err)
	}

	// update with nothing different on the machine runs no chmod
	before := len(fake.Commands())
	if _, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: directoryURN, State: created.Properties, Inputs: checked.Inputs}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || got[0] != sharedRead {
		t.Fatalf("a settled update ran %v", got)
	}
	// update with the sticky bit added applies 3775
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: directoryURN, State: created.Properties, Inputs: sticky})
	if err != nil || updated.Properties.Get("mode").AsString() != "3775" || !fake.Ran("chmod 3775") {
		t.Fatalf("update %+v %v", updated, err)
	}

	// gone from the machine: forgotten
	statOut = ""
	gone, err := s.Read(p.ReadRequest{ID: created.ID, Urn: directoryURN, Properties: updated.Properties})
	if err != nil || gone.ID != "" {
		t.Fatalf("read of a vanished directory %+v %v", gone, err)
	}

	// delete is rmdir, nothing more
	before = len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: directoryURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || got[0] != sharedRmdir {
		t.Fatalf("delete ran %v", got)
	}
}

// A directory with something in it is kept, and the delete fails rather than taking it with it.
func TestDirectoryDeleteKeepsANonEmptyDirectory(t *testing.T) {
	fake := remotetest.New().On(sharedRmdir, remotetest.Reply{Code: 1, Stderr: "rmdir: failed to remove '/srv/shared': Directory not empty\n"})
	s := configured(t, fake)
	err := s.Delete(p.DeleteRequest{ID: "/srv/shared", Urn: directoryURN, Properties: property.NewMap(map[string]property.Value{
		"path": property.New("/srv/shared"), "mode": property.New("2775"), "owner": property.New("root"), "group": property.New("staff"),
	})})
	if err == nil || !strings.Contains(err.Error(), "Directory not empty") {
		t.Fatalf("got %v", err)
	}
	if fake.Ran("rm -") {
		t.Fatal("delete went beyond rmdir")
	}
}

// A file where the directory belongs is refused by name, not reported absent.
func TestDirectoryRefusesAFile(t *testing.T) {
	fake := remotetest.New().On(sharedRead, remotetest.Reply{Stdout: "regular file|644|root|root\n"})
	s := configured(t, fake)
	_, err := s.Read(p.ReadRequest{ID: "/srv/shared", Urn: directoryURN})
	want := "/srv/shared is a regular file on chris@127.0.0.1:20022, not a directory: refusing to guess what to do with it"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
}

// A preview runs nothing on the machine.
func TestDirectoryPreviewRunsNothing(t *testing.T) {
	fake := remotetest.New()
	s := configured(t, fake)
	if _, err := s.Create(p.CreateRequest{Urn: directoryURN, DryRun: true, Properties: directoryInputs(nil)}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Commands()) != 0 {
		t.Fatalf("preview ran %v", fake.Commands())
	}
}

func TestDirectoryQuotesOwnership(t *testing.T) {
	const command = `sudo -n sh -c 'mkdir -p '\''/srv/shared'\'' && chmod 2775 '\''/srv/shared'\'' && chown '\''owner;name:group$name'\'' '\''/srv/shared'\'''`
	fake := remotetest.New().On(command, remotetest.Reply{})
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := directoryInputs(map[string]property.Value{
		"owner": property.New("owner;name"),
		"group": property.New("group$name"),
	})
	if _, err := s.Create(p.CreateRequest{Urn: directoryURN, Properties: inputs}); err != nil {
		t.Fatal(err)
	}
}
