package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

const (
	vaultRead   = `sudo -n sh -c 'test -e '\''/srv/docs/.env.vault'\'' || exit 9; getfacl -p '\''/srv/docs/.env.vault'\'''`
	vaultHeader = "# file: srv/docs/.env.vault\n# owner: root\n# group: rwgroup\n"
	vaultClosed = vaultHeader + "user::rw-\nuser:t3code:r--\ngroup::rw-\nmask::rw-\nother::---\n"
	// what the share's inherited ACL leaves on a new file: rogroup and other can read it
	vaultOpen = "# file: srv/docs/.env.vault\n# owner: chris\n# group: rwgroup\n" +
		"user::rw-\ngroup::rw-\ngroup:rogroup:r--\nmask::rw-\nother::r--\n"
)

// The vault password file's declaration: its owner and group, its mode as the owning entries, and
// one named reader, exactly.
func vaultInputs(extra map[string]property.Value) property.Map {
	m := map[string]property.Value{
		"path":  property.New("/srv/docs/.env.vault"),
		"owner": property.New("root"), "group": property.New("rwgroup"), "exact": property.New(true),
		"entries": entriesValue([3]string{"user", "", "rw-"}, [3]string{"group", "", "rw-"}, [3]string{"other", "", "---"},
			[3]string{"user", "t3code", "r--"}),
	}
	for k, v := range extra {
		m[k] = v
	}
	return property.NewMap(m)
}

// noContent fails if any command could have read or written the path's content.
func noContent(t *testing.T, commands []string) {
	t.Helper()
	for _, c := range commands {
		for _, forbidden := range []string{"cat ", "sha256sum", "head ", "tail ", "base64", "tee ", "dd ", "cp ", "mv ", "touch ", " > ", " < "} {
			if strings.Contains(c, forbidden) {
				t.Fatalf("PosixAcl ran %q, which touches content (%q)", c, forbidden)
			}
		}
	}
}

// A file already closed by hand imports by id as no change, and then a hand edit that reopens it
// is drift that the next update closes again.
func TestPosixAclExactImportAndDrift(t *testing.T) {
	fake := remotetest.New()
	getfacl := vaultClosed
	fake.When(func(c string) bool { return c == vaultRead }, func(string) remotetest.Reply { return remotetest.Reply{Stdout: getfacl} }, 0)
	removeRogroup := `sudo -n sh -c 'setfacl -x '\''group:rogroup'\'' '\''/srv/docs/.env.vault'\'''`
	fake.When(func(c string) bool { return c == removeRogroup }, func(string) remotetest.Reply {
		getfacl = vaultClosed
		return remotetest.Reply{}
	}, 0)
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	checked, err := s.Check(p.CheckRequest{Urn: aclURN, Inputs: vaultInputs(nil)})
	if err != nil || len(checked.Failures) != 0 {
		t.Fatalf("check %+v %v", checked, err)
	}

	imported, err := s.Read(p.ReadRequest{ID: "/srv/docs/.env.vault", Urn: aclURN})
	if err != nil {
		t.Fatal(err)
	}
	// the import offers only the named entries as inputs, as it always has
	if n := imported.Inputs.Get("entries").AsArray().Len(); n != 1 {
		t.Fatalf("import inputs list %d entries, want the one named entry", n)
	}
	diff, err := s.Diff(p.DiffRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: imported.Properties, Inputs: checked.Inputs})
	if err != nil || diff.HasChanges {
		t.Fatalf("importing a file that already matches diffs: %+v %v", diff, err)
	}

	// the owner, a base entry or an extra named entry differing is each a change on the import
	for _, c := range []struct {
		name    string
		getfacl string
		want    string
	}{
		{"owner", strings.Replace(vaultClosed, "# owner: root", "# owner: chris", 1), "owner"},
		{"other bits", strings.Replace(vaultClosed, "other::---", "other::r--", 1), "entries"},
		{"an undeclared reader", strings.Replace(vaultClosed, "mask::", "group:rogroup:r--\nmask::", 1), "entries"},
	} {
		getfacl = c.getfacl
		read, err := s.Read(p.ReadRequest{ID: "/srv/docs/.env.vault", Urn: aclURN})
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.Diff(p.DiffRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: read.Properties, Inputs: checked.Inputs})
		if err != nil || d.DetailedDiff[c.want].Kind != p.Update {
			t.Fatalf("%s: %+v %v", c.name, d, err)
		}
	}

	// an update from the adopted state removes the undeclared reader, because the declaration is exact
	getfacl = strings.Replace(vaultClosed, "mask::", "group:rogroup:r--\nmask::", 1)
	drifted, err := s.Read(p.ReadRequest{ID: "/srv/docs/.env.vault", Urn: aclURN})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.Update(p.UpdateRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: drifted.Properties, Inputs: checked.Inputs})
	if err != nil {
		t.Fatal(err)
	}
	if !fake.Ran("setfacl -x '\\''group:rogroup'\\''") || fake.Ran("chown") || fake.Ran("setfacl -m") {
		t.Fatalf("update ran %v", fake.Commands())
	}
	if updated.Properties.Get("extra").AsArray().Len() != 0 || !updated.Properties.Get("exact").AsBool() {
		t.Fatalf("updated %v", updated.Properties)
	}

	// refresh after the update: no change, and the same reader reappearing is drift again
	read, err := s.Read(p.ReadRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, Properties: updated.Properties, Inputs: checked.Inputs})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: read.Properties, Inputs: checked.Inputs}); err != nil || d.HasChanges {
		t.Fatalf("after the update: %+v %v", d, err)
	}
	getfacl = strings.Replace(vaultClosed, "mask::", "group:rogroup:r--\nmask::", 1)
	reopened, err := s.Read(p.ReadRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, Properties: updated.Properties, Inputs: checked.Inputs})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: reopened.Properties, Inputs: checked.Inputs}); err != nil || d.DetailedDiff["entries"].Kind != p.Update {
		t.Fatalf("a reader added by hand: %+v %v", d, err)
	}
	// without exact, the same reader is somebody else's and no change
	notExact := vaultInputs(map[string]property.Value{"exact": property.New(false)})
	if d, err := s.Diff(p.DiffRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, State: imported.Properties, Inputs: notExact}); err != nil || d.HasChanges {
		t.Fatalf("not exact, on the import: %+v %v", d, err)
	}

	// delete removes the one named entry it declared, and leaves the mode and owner
	before := len(fake.Commands())
	removeT3code := `sudo -n sh -c 'setfacl -x '\''user:t3code'\'' '\''/srv/docs/.env.vault'\'''`
	fake.On(removeT3code, remotetest.Reply{})
	if err := s.Delete(p.DeleteRequest{ID: "/srv/docs/.env.vault", Urn: aclURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || got[0] != removeT3code {
		t.Fatalf("delete ran %v", got)
	}
	noContent(t, fake.Commands())
}

// Creating on a file the share left open: one chown of what differs, one setfacl that removes the inherited reader
// and sets the mode entries and the named one, then a read that proves it.
func TestPosixAclExactCreate(t *testing.T) {
	fake := remotetest.New()
	getfacl := vaultOpen
	fake.When(func(c string) bool { return c == vaultRead }, func(string) remotetest.Reply { return remotetest.Reply{Stdout: getfacl} }, 0)
	// the group is already rwgroup, so only the owner changes
	apply := `sudo -n sh -c 'chown '\''root'\'' '\''/srv/docs/.env.vault'\'' && ` +
		`setfacl -x '\''group:rogroup'\'' '\''/srv/docs/.env.vault'\'' && ` +
		`setfacl -m '\''other::---,user:t3code:r--'\'' '\''/srv/docs/.env.vault'\'''`
	fake.When(func(c string) bool { return c == apply }, func(string) remotetest.Reply {
		getfacl = vaultClosed
		return remotetest.Reply{}
	}, 0)
	s := configured(t, fake)
	created, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: vaultInputs(nil)})
	if err != nil {
		t.Fatalf("%v\nran %v", err, fake.Commands())
	}
	if created.Properties.Get("owner").AsString() != "root" || created.Properties.Get("group").AsString() != "rwgroup" {
		t.Fatalf("created %v", created.Properties)
	}
	if got := fake.Commands(); len(got) != 3 || got[1] != apply {
		t.Fatalf("create ran %v", got)
	}
	noContent(t, fake.Commands())
}

// A chown that does not take is an error, not a success.
func TestPosixAclOwnershipChecksTheEffect(t *testing.T) {
	fake := remotetest.New().On(vaultRead, remotetest.Reply{Stdout: strings.Replace(vaultClosed, "# owner: root", "# owner: chris", 1)}).
		On(`sudo -n sh -c 'chown '\''root'\'' '\''/srv/docs/.env.vault'\'''`, remotetest.Reply{})
	s := configured(t, fake)
	inputs := vaultInputs(nil).Delete("group")
	_, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: inputs})
	if err == nil || !strings.Contains(err.Error(), "set the ownership of /srv/docs/.env.vault at chris@127.0.0.1:20022 to root: and getfacl says chris:rwgroup") {
		t.Fatalf("got %v", err)
	}
}

// Numeric ids and empty names are refused at preview, and a named deny entry is an ordinary entry.
func TestPosixAclOwnershipCheck(t *testing.T) {
	s := configuredServer(t, remotetest.New())
	numeric, err := s.Check(p.CheckRequest{Urn: aclURN, Inputs: vaultInputs(map[string]property.Value{"owner": property.New("0")})})
	if err != nil || len(numeric.Failures) != 1 || numeric.Failures[0].Property != "owner" || !strings.Contains(numeric.Failures[0].Reason, "Write the name") {
		t.Fatalf("numeric owner %+v %v", numeric, err)
	}
	empty, err := s.Check(p.CheckRequest{Urn: aclURN, Inputs: vaultInputs(map[string]property.Value{"group": property.New("")})})
	if err != nil || len(empty.Failures) != 1 || empty.Failures[0].Property != "group" {
		t.Fatalf("empty group %+v %v", empty, err)
	}
	deny, err := s.Check(p.CheckRequest{Urn: aclURN, Inputs: vaultInputs(map[string]property.Value{
		"entries": entriesValue([3]string{"user", "t3code", "r--"}, [3]string{"user", "jonmc", "---"}),
	})})
	if err != nil || len(deny.Failures) != 0 {
		t.Fatalf("deny entry %+v %v", deny, err)
	}
}

// A preview runs nothing on the machine.
func TestPosixAclExactPreviewRunsNothing(t *testing.T) {
	fake := remotetest.New()
	s := configured(t, fake)
	if _, err := s.Create(p.CreateRequest{Urn: aclURN, DryRun: true, Properties: vaultInputs(nil)}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Commands()) != 0 {
		t.Fatalf("preview ran %v", fake.Commands())
	}
}
