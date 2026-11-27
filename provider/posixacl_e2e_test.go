package homelab

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var aclURN = presource.URN("urn:pulumi:s::p::homelab:index:PosixAcl::pool")

const (
	poolRead    = `sudo -n sh -c 'test -e '\''/mnt/pool'\'' || exit 9; getfacl -pc '\''/mnt/pool'\'''`
	poolSet     = `sudo -n sh -c 'setfacl -m '\''user:svc:rwx,default:user:svc:rwx'\'' '\''/mnt/pool'\'''`
	poolSwap    = `sudo -n sh -c 'setfacl -x '\''user:svc'\'' '\''/mnt/pool'\'' && setfacl -m '\''group:ro:r-x'\'' '\''/mnt/pool'\'''`
	poolRemove  = `sudo -n sh -c 'setfacl -x '\''group:ro,default:user:svc'\'' '\''/mnt/pool'\'''`
	poolWalk    = `sudo -n sh -c 'find '\''/mnt/pool'\'' -xdev \( -path '\''/mnt/pool/var'\'' \) -prune -o -type d -group '\''rw'\'' -perm 2755 -print -exec chmod 2775 {} + | wc -l'`
	poolCheck   = `sudo -n sh -c 'find '\''/mnt/pool'\'' -xdev \( -path '\''/mnt/pool/var'\'' \) -prune -o -type d -group '\''rw'\'' -perm 2755 -print | wc -l'`
	bareGetfacl = "user::rwx\ngroup::r-x\nother::r-x\n"
)

func entriesValue(entries ...[3]string) property.Value {
	out := []property.Value{}
	for _, e := range entries {
		m := map[string]property.Value{"type": property.New(e[0]), "perms": property.New(e[2])}
		if e[1] != "" {
			m["name"] = property.New(e[1])
		}
		out = append(out, property.New(property.NewMap(m)))
	}
	return property.New(out)
}

func TestPosixAclLifecycle(t *testing.T) {
	fake := remotetest.New()
	getfacl, suppress, left := bareGetfacl, false, "0\n"
	fake.When(func(c string) bool { return c == poolRead }, func(string) remotetest.Reply { return remotetest.Reply{Stdout: getfacl} }, 0)
	fake.When(func(c string) bool { return c == poolSet }, func(string) remotetest.Reply {
		if suppress {
			return remotetest.Reply{}
		}
		getfacl = bareGetfacl + "user:svc:rwx\nmask::rwx\ndefault:user::rwx\ndefault:user:svc:rwx\n"
		return remotetest.Reply{}
	}, 0)
	fake.When(func(c string) bool { return c == poolSwap }, func(string) remotetest.Reply {
		getfacl = bareGetfacl + "group:ro:r-x\nmask::rwx\ndefault:user:svc:rwx\n"
		return remotetest.Reply{}
	}, 0)
	fake.On(poolWalk, remotetest.Reply{Stdout: "3\n"}).When(func(c string) bool { return c == poolCheck }, func(string) remotetest.Reply { return remotetest.Reply{Stdout: left} }, 0).On(poolRemove, remotetest.Reply{})
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := property.NewMap(map[string]property.Value{
		"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"user", "svc", "7"}),
		"defaultEntries": entriesValue([3]string{"user", "svc", "rwx"}),
	})

	// check refuses what may not be declared, at preview
	bad, err := s.Check(p.CheckRequest{Urn: aclURN, Inputs: property.NewMap(map[string]property.Value{
		"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"mask", "", "rwx"}),
		"normalise": property.New(property.NewMap(map[string]property.Value{"group": property.New("g"), "from": property.New("755"), "to": property.New("0755")})),
	})})
	if err != nil || len(bad.Failures) != 2 || bad.Failures[0].Property != "entries" || !strings.HasPrefix(bad.Failures[0].Reason, "mask:: a mask is computed") ||
		bad.Failures[1].Property != "normalise" {
		t.Fatalf("check %+v %v", bad, err)
	}

	created, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	actual := created.Properties.Get("actual").AsArray().Get(0).AsMap()
	if created.ID != "/mnt/pool" || actual.Get("effective").AsString() != "rwx" || created.Properties.Get("entries").AsArray().Get(0).AsMap().Get("perms").AsString() != "rwx" {
		t.Fatalf("created %v", created.Properties)
	}

	// a chmod recomputed the mask: read shows it, and diff sees the drift
	getfacl = bareGetfacl + "user:svc:rwx\t#effective:r-x\nmask::r-x\ndefault:user::rwx\ndefault:user:svc:rwx\n"
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: aclURN, Properties: created.Properties, Inputs: inputs})
	if err != nil || read.Properties.Get("actual").AsArray().Get(0).AsMap().Get("effective").AsString() != "r-x" {
		t.Fatalf("read %+v %v", read, err)
	}
	drift, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: read.Properties, Inputs: inputs})
	if err != nil || !drift.HasChanges || drift.DetailedDiff["entries"].Kind != p.Update {
		t.Fatalf("drift diff %+v %v", drift, err)
	}
	same, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: created.Properties, Inputs: inputs})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged diff %+v %v", same, err)
	}
	moved := property.NewMap(map[string]property.Value{"path": property.New("/mnt/other")})
	mv, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: created.Properties, Inputs: moved})
	if err != nil || !mv.DeleteBeforeReplace || mv.DetailedDiff["path"].Kind != p.UpdateReplace {
		t.Fatalf("path diff %+v %v", mv, err)
	}

	// a mask suppressing a set entry is an error naming the entry
	getfacl, suppress = bareGetfacl, true
	_, err = s.Create(p.CreateRequest{Urn: aclURN, Properties: inputs})
	if err == nil || !strings.Contains(err.Error(), "set user:svc:rwx, default:user:svc:rwx on /mnt/pool at chris@127.0.0.1:20022 and the ACL still does not grant it") {
		t.Fatalf("suppressed: %v", err)
	}

	suppress = false
	// update: svc dropped from access and ro added, with a normalise rule
	getfacl = bareGetfacl + "user:svc:rwx\nmask::rwx\ndefault:user::rwx\ndefault:user:svc:rwx\n"
	next := property.NewMap(map[string]property.Value{
		"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"group", "ro", "r-x"}),
		"defaultEntries": entriesValue([3]string{"user", "svc", "rwx"}),
		"normalise": property.New(property.NewMap(map[string]property.Value{
			"group": property.New("rw"), "from": property.New("2755"), "to": property.New("2775"),
			"ignore": property.New([]property.Value{property.New("var/")}),
		})),
	})
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: aclURN, State: created.Properties, Inputs: next})
	if err != nil || updated.Properties.Get("normalised").AsNumber() != 3 || updated.Properties.Get("normalise").AsMap().Get("ignore").AsArray().Get(0).AsString() != "var" {
		t.Fatalf("update %+v %v", updated, err)
	}

	// directories still at the old mode after the walk are an error
	left = "2\n"
	_, err = s.Update(p.UpdateRequest{ID: created.ID, Urn: aclURN, State: updated.Properties, Inputs: next})
	if err == nil || !strings.Contains(err.Error(), "2 directories under /mnt/pool at chris@127.0.0.1:20022 are still 2755 after being set to 2775") {
		t.Fatalf("left: %v", err)
	}

	// delete removes only what it declared
	before := len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: aclURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || got[0] != poolRemove {
		t.Fatalf("delete ran %v", got)
	}

	// a path that is not there: create refuses, read forgets
	fake2 := remotetest.New().On(poolRead, remotetest.Reply{Code: 9})
	s2 := configured(t, fake2)
	if _, err := s2.Create(p.CreateRequest{Urn: aclURN, Properties: inputs}); err == nil || !strings.Contains(err.Error(), "nothing exists at /mnt/pool on chris@127.0.0.1:20022, so there is no ACL to set on it") {
		t.Fatalf("create on nothing: %v", err)
	}
	if gone, err := s2.Read(p.ReadRequest{ID: "/mnt/pool", Urn: aclURN, Properties: updated.Properties}); err != nil || gone.ID != "" {
		t.Fatalf("read of nothing %+v %v", gone, err)
	}

	// getfacl missing names the package
	fake3 := remotetest.New().On(poolRead, remotetest.Reply{Code: 127})
	if _, err := configured(t, fake3).Read(p.ReadRequest{ID: "/mnt/pool", Urn: aclURN}); err == nil || !strings.Contains(err.Error(), "getfacl is not installed on chris@127.0.0.1:20022") {
		t.Fatalf("no getfacl: %v", err)
	}
}

// blockingRunner holds the normalise walk until another resource's command has run.
type blockingRunner struct {
	otherRan  chan struct{}
	closeOnce sync.Once
}

func (b *blockingRunner) Run(ctx context.Context, command string, _ []byte) ([]byte, []byte, int, error) {
	switch {
	case command == poolRead:
		return []byte(bareGetfacl), nil, 0, nil
	case strings.Contains(command, "-exec chmod"):
		select {
		case <-b.otherRan:
			return []byte("1\n"), nil, 0, nil
		case <-time.After(5 * time.Second):
			return []byte("1\n"), nil, 0, nil
		}
	case strings.Contains(command, "find "):
		return []byte("0\n"), nil, 0, nil
	case strings.HasPrefix(command, "getent group"):
		b.closeOnce.Do(func() { close(b.otherRan) })
		return []byte("storage:x:1002:\n"), nil, 0, nil
	}
	return nil, nil, 0, nil
}

// A long normalise walk does not hold up other resources: a Group is created and read while the
// walk is still running.
func TestPosixAclNormaliseDoesNotHoldUpOthers(t *testing.T) {
	runner := &blockingRunner{otherRan: make(chan struct{})}
	s := newServer(t, (&capture{runner: runner}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: property.NewMap(map[string]property.Value{
			"path": property.New("/mnt/pool"),
			"normalise": property.New(property.NewMap(map[string]property.Value{
				"group": property.New("rw"), "from": property.New("2755"), "to": property.New("2775"),
			})),
		})})
		done <- err
	}()
	if _, err := s.Read(p.ReadRequest{ID: "storage", Urn: groupURN}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("the walk waited for its timeout (%s): the other resource was held up behind it", elapsed)
	}
}

// Importing a path whose ACL already grants the declaration diffs as no change, by id alone and
// with the declaration, and the adopted resource takes no ownership of entries it did not declare.
func TestPosixAclImport(t *testing.T) {
	onMachine := bareGetfacl + "user:svc:rwx\ngroup:other:r-x\nmask::rwx\ndefault:user::rwx\ndefault:user:svc:rwx\n"
	fake := remotetest.New().On(poolRead, remotetest.Reply{Stdout: onMachine})
	s := configured(t, fake)
	declared := property.NewMap(map[string]property.Value{
		"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"user", "svc", "7"}),
		"defaultEntries": entriesValue([3]string{"user", "svc", "rwx"}),
	})

	byID, err := s.Read(p.ReadRequest{ID: "/mnt/pool", Urn: aclURN})
	if err != nil {
		t.Fatal(err)
	}
	if n := byID.Inputs.Get("entries").AsArray().Len(); n != 2 {
		t.Fatalf("import inputs list %d entries, want both named entries on the path", n)
	}
	for _, read := range []struct {
		name  string
		state property.Map
	}{{"by id", byID.Properties}} {
		diff, err := s.Diff(p.DiffRequest{ID: "/mnt/pool", Urn: aclURN, State: read.state, Inputs: declared})
		if err != nil || diff.HasChanges {
			t.Fatalf("%s: %+v %v", read.name, diff, err)
		}
	}
	withDecl, err := s.Read(p.ReadRequest{ID: "/mnt/pool", Urn: aclURN, Inputs: declared})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "/mnt/pool", Urn: aclURN, State: withDecl.Properties, Inputs: declared}); err != nil || diff.HasChanges {
		t.Fatalf("with the declaration: %+v %v", diff, err)
	}
	// a refresh of the adopted state keeps seeing no change
	refreshed, err := s.Read(p.ReadRequest{ID: "/mnt/pool", Urn: aclURN, Properties: byID.Properties, Inputs: declared})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "/mnt/pool", Urn: aclURN, State: refreshed.Properties, Inputs: declared}); err != nil || diff.HasChanges {
		t.Fatalf("after refresh: %+v %v", diff, err)
	}
	// a declaration the path does not grant is a change
	more := property.NewMap(map[string]property.Value{"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"user", "new", "rwx"})})
	if diff, err := s.Diff(p.DiffRequest{ID: "/mnt/pool", Urn: aclURN, State: byID.Properties, Inputs: more}); err != nil || diff.DetailedDiff["entries"].Kind != p.Update {
		t.Fatalf("ungranted: %+v %v", diff, err)
	}
	// an update after the import removes nothing: group:other was never this resource's
	updated, err := s.Update(p.UpdateRequest{ID: "/mnt/pool", Urn: aclURN, State: byID.Properties, Inputs: declared})
	if err != nil || fake.Ran("setfacl") {
		t.Fatalf("update after import ran %v: %v", fake.Commands(), err)
	}
	if n := updated.Properties.Get("entries").AsArray().Len(); n != 1 {
		t.Fatalf("owns %d entries after the update, want the one declared", n)
	}
}
