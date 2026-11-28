package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var symlinkURN = presource.URN("urn:pulumi:s::p::homelab:index:Symlink::journal")

const (
	journalRead   = `sudo -n sh -c 'if pointsAt=$(readlink '\''/var/log/journal'\'' 2>/dev/null); then printf '\''%s'\'' "$pointsAt"; exit 0; fi; if [ -e '\''/var/log/journal'\'' ] || [ -L '\''/var/log/journal'\'' ]; then stat -c '\''%F'\'' '\''/var/log/journal'\''; exit 8; fi; exit 9'`
	journalLink   = `sudo -n sh -c 'mkdir -p '\''/var/log'\'' && ln -sfn '\''/mnt/data/journal'\'' '\''/var/log/journal'\'''`
	journalRelink = `sudo -n sh -c 'mkdir -p '\''/var/log'\'' && ln -sfn '\''/mnt/other/journal'\'' '\''/var/log/journal'\'''`
	journalHead   = `sudo -n sh -c 'head -c 4097 '\''/var/log/journal'\'''`
)

var journalDelete = asRoot(symlinkDeleteCommand("/var/log/journal", nil))

func symlinkInputs(target string, accept bool) property.Map {
	return property.NewMap(map[string]property.Value{
		"path": property.New("/var/log/journal"), "target": property.New(target), "acceptLauncherScript": property.New(accept),
	})
}

// Create, read, update, diff and delete through the gRPC surface with the fake runner.
func TestSymlinkLifecycle(t *testing.T) {
	fake := remotetest.New()
	pointsAt := ""
	fake.When(func(c string) bool { return c == journalRead }, func(string) remotetest.Reply {
		if pointsAt == "" {
			return remotetest.Reply{Code: 9}
		}
		return remotetest.Reply{Stdout: pointsAt}
	}, 0)
	fake.When(func(c string) bool { return c == journalLink }, func(string) remotetest.Reply {
		pointsAt = "/mnt/data/journal"
		return remotetest.Reply{}
	}, 0)
	fake.When(func(c string) bool { return c == journalRelink }, func(string) remotetest.Reply {
		pointsAt = "/mnt/other/journal"
		return remotetest.Reply{}
	}, 0)
	fake.On(journalDelete, remotetest.Reply{})
	s := configured(t, fake)

	// create: nothing there, so it is linked
	created, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: symlinkInputs("/mnt/data/journal", false)})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "/var/log/journal" || created.Properties.Get("target").AsString() != "/mnt/data/journal" {
		t.Fatalf("created %s %v", created.ID, created.Properties)
	}
	if !fake.Ran("ln -sfn") {
		t.Fatal("create did not link")
	}

	// creating again over a link that already points there runs no ln
	before := len(fake.Commands())
	if _, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: symlinkInputs("/mnt/data/journal", false)}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || got[0] != journalRead {
		t.Fatalf("adopting ran %v", got)
	}

	// read: no drift
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: symlinkURN, Properties: created.Properties, Inputs: symlinkInputs("/mnt/data/journal", false)})
	if err != nil || read.ID != created.ID || read.Properties.Get("target").AsString() != "/mnt/data/journal" {
		t.Fatalf("read %+v %v", read, err)
	}

	// import: no state, and the inputs come from the machine
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: symlinkURN})
	if err != nil || imported.Inputs.Get("path").AsString() != "/var/log/journal" || imported.Inputs.Get("target").AsString() != "/mnt/data/journal" {
		t.Fatalf("import %+v %v", imported, err)
	}

	// diff: same is no change; a new target is an update; a new path replaces, deleting first
	same, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: symlinkURN, State: created.Properties, Inputs: symlinkInputs("/mnt/data/journal", false)})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged diff %+v %v", same, err)
	}
	retarget, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: symlinkURN, State: created.Properties, Inputs: symlinkInputs("/mnt/other/journal", false)})
	if err != nil || !retarget.HasChanges || retarget.DetailedDiff["target"].Kind != p.Update {
		t.Fatalf("target diff %+v %v", retarget, err)
	}
	moved := property.NewMap(map[string]property.Value{"path": property.New("/var/log/journal2"), "target": property.New("/mnt/data/journal")})
	mv, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: symlinkURN, State: created.Properties, Inputs: moved})
	if err != nil || !mv.HasChanges || !mv.DeleteBeforeReplace || mv.DetailedDiff["path"].Kind != p.UpdateReplace {
		t.Fatalf("path diff %+v %v", mv, err)
	}

	// update relinks
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: symlinkURN, State: created.Properties, Inputs: symlinkInputs("/mnt/other/journal", false)})
	if err != nil || updated.Properties.Get("target").AsString() != "/mnt/other/journal" || pointsAt != "/mnt/other/journal" {
		t.Fatalf("update %+v %v", updated, err)
	}

	// read after the link vanished: forgotten
	pointsAt = ""
	gone, err := s.Read(p.ReadRequest{ID: created.ID, Urn: symlinkURN, Properties: updated.Properties})
	if err != nil || gone.ID != "" {
		t.Fatalf("read of a vanished link %+v %v", gone, err)
	}

	// delete removes the path only while it is a link
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: symlinkURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if !fake.Ran("mv -T --") {
		t.Fatalf("delete ran %v", fake.Commands())
	}
}

// A real directory where the link belongs is refused by name, and nothing is linked over it.
func TestSymlinkRefusesSomethingReal(t *testing.T) {
	fake := remotetest.New().On(journalRead, remotetest.Reply{Code: 8, Stdout: "directory\n"})
	s := configured(t, fake)
	_, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: symlinkInputs("/mnt/data/journal", false)})
	want := "/var/log/journal on chris@127.0.0.1:20022 is a directory, not a symlink. Refusing to replace it: whatever is in there is data this resource did not put there."
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
	if fake.Ran("ln -sfn") {
		t.Fatal("linked over a directory")
	}
	_, err = s.Read(p.ReadRequest{ID: "/var/log/journal", Urn: symlinkURN})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("read got %v", err)
	}
}

// An exit that is none of the three answers is an error carrying stderr.
func TestSymlinkUnreadable(t *testing.T) {
	fake := remotetest.New().On(journalRead, remotetest.Reply{Code: 1, Stderr: "sudo: a password is required\n"})
	s := configured(t, fake)
	_, err := s.Read(p.ReadRequest{ID: "/var/log/journal", Urn: symlinkURN})
	if err == nil || !strings.Contains(err.Error(), "could not read /var/log/journal: sudo: a password is required") {
		t.Fatalf("got %v", err)
	}
}

// With acceptLauncherScript, a launcher script is read back as its program and never replaced; a
// regular file that is not one is still refused.
func TestSymlinkAcceptsALauncherScript(t *testing.T) {
	fake := remotetest.New().
		On(journalRead, remotetest.Reply{Code: 8, Stdout: "regular file\n"}).
		Once(journalHead, remotetest.Reply{Stdout: "#!/bin/sh\nexec '/opt/t3/0.2/t3' \"$@\"\n"}).
		Once(journalHead, remotetest.Reply{Stdout: "\x7fELF"})
	s := configured(t, fake)
	created, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: symlinkInputs("/mnt/data/journal", true)})
	if err != nil {
		t.Fatal(err)
	}
	if fake.Ran("ln -sfn") {
		t.Fatal("a launcher script was replaced")
	}
	_, err = s.Read(p.ReadRequest{ID: created.ID, Urn: symlinkURN, Properties: created.Properties})
	if err == nil || !strings.Contains(err.Error(), "is a regular file, not a symlink") {
		t.Fatalf("a binary was accepted as a launcher: %v", err)
	}

	// without the flag, the script is refused like any other file and its contents are not read
	plain := remotetest.New().On(journalRead, remotetest.Reply{Code: 8, Stdout: "regular file\n"})
	_, err = configured(t, plain).Create(p.CreateRequest{Urn: symlinkURN, Properties: symlinkInputs("/mnt/data/journal", false)})
	if err == nil || plain.Ran("head -c") {
		t.Fatalf("got %v, ran %v", err, plain.Commands())
	}
}

// The launcher's program is the target read back.
func TestSymlinkReadsALauncherScriptsProgram(t *testing.T) {
	fake := remotetest.New().
		On(journalRead, remotetest.Reply{Code: 8, Stdout: "regular file\n"}).
		On(journalHead, remotetest.Reply{Stdout: "#!/bin/sh\nexec '/opt/t3/0.2/t3' \"$@\"\n"})
	s := configured(t, fake)
	state := property.NewMap(map[string]property.Value{
		"path": property.New("/var/log/journal"), "target": property.New("/opt/t3/0.1/t3"), "acceptLauncherScript": property.New(true),
	})
	read, err := s.Read(p.ReadRequest{ID: "/var/log/journal", Urn: symlinkURN, Properties: state})
	if err != nil || read.Properties.Get("target").AsString() != "/opt/t3/0.2/t3" {
		t.Fatalf("read %+v %v", read, err)
	}
}

// A preview runs nothing on the machine.
func TestSymlinkPreviewRunsNothing(t *testing.T) {
	fake := remotetest.New()
	s := configured(t, fake)
	if _, err := s.Create(p.CreateRequest{Urn: symlinkURN, DryRun: true, Properties: symlinkInputs("/mnt/data/journal", false)}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Commands()) != 0 {
		t.Fatalf("preview ran %v", fake.Commands())
	}
}

// An import of a launcher script names it with @launcher: it reads back as its program, keeps the
// flag for later refreshes, and a declaration that accepts launcher scripts diffs clean. Without the
// suffix the script is a file in the way, as it is for any read without the flag.
func TestSymlinkImportsALauncherScript(t *testing.T) {
	fake := remotetest.New().
		On(journalRead, remotetest.Reply{Code: 8, Stdout: "regular file\n"}).
		On(journalHead, remotetest.Reply{Stdout: "#!/bin/sh\nexec '/opt/t3/0.2/t3' \"$@\"\n"})
	s := configured(t, fake)
	read, err := s.Read(p.ReadRequest{ID: "/var/log/journal@launcher", Urn: symlinkURN})
	if err != nil || read.ID != "/var/log/journal" || read.Properties.Get("target").AsString() != "/opt/t3/0.2/t3" ||
		!read.Properties.Get("acceptLauncherScript").AsBool() {
		t.Fatalf("read %+v %v", read, err)
	}
	declared := property.NewMap(map[string]property.Value{
		"path": property.New("/var/log/journal"), "target": property.New("/opt/t3/0.2/t3"), "acceptLauncherScript": property.New(true),
	})
	if diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: symlinkURN, State: read.Properties, Inputs: declared}); err != nil || diff.HasChanges {
		t.Fatalf("diff %+v %v", diff, err)
	}
	if _, err := s.Read(p.ReadRequest{ID: "/var/log/journal", Urn: symlinkURN}); err == nil {
		t.Fatal("an import without @launcher accepted a launcher script")
	}
}
