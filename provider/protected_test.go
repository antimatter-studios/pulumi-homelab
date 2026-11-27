package homelab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

func strp(s string) *string { return &s }

// Ported from src/resources/protected.test.ts: substituting the temporary file into an argv.
func TestSubstituteArgv(t *testing.T) {
	cases := []struct {
		name, where string
		argv, want  []string
	}{
		{"replaces every occurrence, not only the first", "/tmp/blob",
			[]string{"tool", "--out", "{}", "--also", "{}"}, []string{"tool", "--out", "/tmp/blob", "--also", "/tmp/blob"}},
		// `--out={}` is one argv entry, and a replace that only matched whole entries would miss it
		{"substitutes inside an argument rather than only as a whole one", "/tmp/blob",
			[]string{"tool", "--out={}"}, []string{"tool", "--out=/tmp/blob"}},
		{"leaves an argv with nothing to substitute exactly as it was", "/tmp/blob",
			[]string{"pass", "show", "secret"}, []string{"pass", "show", "secret"}},
		// the point of an argv rather than a command string: a path with a space is one argument
		{"does not quote or escape, because there is no shell to protect from", "/tmp/a dir/blob",
			[]string{"tool", "{}"}, []string{"tool", "/tmp/a dir/blob"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := substituteArgv(c.argv, c.where); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Ported from src/resources/protected.test.ts: refusing a source that cannot work.
func TestSourceRefusal(t *testing.T) {
	cases := []struct {
		name   string
		source Source
		want   string
	}{
		{"accepts a file source with a path", Source{Kind: "file", Path: strp("secrets/token.json")}, ""},
		{"refuses a file source with no path", Source{Kind: "file", Path: strp("")}, "source.path is empty"},
		{"accepts an exec source", Source{Kind: "exec", Argv: []string{"pass", "show", "token"}}, ""},
		{"refuses an empty argv, which execFileSync reports as a missing program", Source{Kind: "exec", Argv: []string{}},
			"source.argv is empty for kind 'exec'"},
		{"accepts an execFile source that says where to write",
			Source{Kind: "execFile", Argv: []string{"trove", "get", "file", "--out", "{}", "entry"}}, ""},
		// the command runs, succeeds, writes its material somewhere else entirely, and the provider
		// reads the empty temporary file it made: an empty secret, written successfully
		{"refuses an execFile source with no place to write it",
			Source{Kind: "execFile", Argv: []string{"trove", "get", "file", "entry"}},
			"source.argv for kind 'execFile' has no '{}' for the provider to substitute"},
		{"accepts `{}` embedded in a longer argument", Source{Kind: "execFile", Argv: []string{"tool", "--out={}"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sourceRefusal(c.source); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
	// Go has no union type to rule these out, so they are refused here instead
	if got := sourceRefusal(Source{Kind: "file"}); got != "source.path is empty" {
		t.Errorf("a file source with no path at all: %q", got)
	}

}

// Ported from src/resources/protected.test.ts: whether the material has to be fetched at all.
func TestNeedsMaterial(t *testing.T) {
	t.Run("fetches under always, which is what a digest is for", func(t *testing.T) {
		if !needsMaterial("always") {
			t.Fatal("always does not fetch")
		}
	})
	// not an optimisation: fetching would unlock a vault and hold a secret in memory to compute an
	// answer that is then discarded, during a preview
	t.Run("does not fetch under once", func(t *testing.T) {
		if needsMaterial("once") {
			t.Fatal("once fetches")
		}
	})
	// an empty string would read as "digest not computed yet" and invite a comparison against one
	t.Run("marks unfetched state with a word rather than an empty digest", func(t *testing.T) {
		if seeded != "seeded" {
			t.Fatalf("seeded is %q", seeded)
		}
	})
}

// Ported from src/resources/protected.test.ts: the source union is data.
func TestSourceHoldsNoMaterial(t *testing.T) {
	sources := []Source{
		{Kind: "file", Path: strp("secrets/claude-p.credentials.json")},
		{Kind: "exec", Argv: []string{"op", "read", "op://vault/item/field"}},
		{Kind: "execFile", Argv: []string{"trove", "get", "file", "--name", "key", "--out", "{}", "entry"}},
	}
	// JSON round-trips, which is the property a resource input has to have and a closure does not
	encoded, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	var back []Source
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, sources) {
		t.Fatalf("got %+v, want %+v", back, sources)
	}
}

var protectedURN = presource.URN("urn:pulumi:s::p::homelab:index:ProtectedFile::key")

func protectedInputs(source property.Value, enforce string) property.Map {
	return property.NewMap(map[string]property.Value{
		"path": property.New("/home/chris/.ssh/id_ed25519"), "source": source,
		"owner": property.New("chris"), "group": property.New("chris"), "enforce": property.New(enforce),
	})
}

func fileSource(path string) property.Value {
	return property.New(map[string]property.Value{"kind": property.New("file"), "path": property.New(path)})
}

// The whole promise: the material reaches the machine as stdin data, never in a command, and never
// in the state, whose only trace of it is the digest.
func TestProtectedFileLifecycle(t *testing.T) {
	const secret = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret material\n"
	dir := t.TempDir()
	src := filepath.Join(dir, "key")
	if err := os.WriteFile(src, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	state := ProtectedFileState{Path: "/home/chris/.ssh/id_ed25519", Mode: "0600", Owner: "chris", Group: "chris", Enforce: "always"}
	onDisk := ""
	fake := remotetest.New()
	fake.When(func(c string) bool { return c == asRoot(writeProtectedCommand(state)) }, func(string) remotetest.Reply {
		onDisk = fake.Calls[len(fake.Calls)-1].Stdin
		return remotetest.Reply{}
	}, 0)
	fake.On(asRoot(readProtectedCommand(state.Path, "always")), remotetest.Reply{Stdout: digestOf([]byte(secret)) + "\n600 chris chris\n"})
	fake.On(asRoot(readProtectedCommand(state.Path, "once")), remotetest.Reply{Stdout: "600 chris chris\n"})
	fake.On(asRoot(refaceCommand(ProtectedFileState{Path: state.Path, Mode: "0600", Owner: "chris", Group: "chris"})), remotetest.Reply{})
	fake.On(asRoot("rm -f '/home/chris/.ssh/id_ed25519'"), remotetest.Reply{})
	s := configured(t, fake)

	created, err := s.Create(p.CreateRequest{Urn: protectedURN, Properties: protectedInputs(fileSource(src), "always")})
	if err != nil {
		t.Fatal(err)
	}
	if onDisk != secret {
		t.Fatalf("wrote %q", onDisk)
	}
	if got := created.Properties.Get("digest").AsString(); got != digestOf([]byte(secret)) {
		t.Fatalf("digest %s", got)
	}
	for _, c := range fake.Calls {
		if strings.Contains(c.Command, "secret material") {
			t.Fatalf("the material went into a command: %s", c.Command)
		}
	}
	if strings.Contains(created.Properties.GoString(), "secret material") {
		t.Fatal("the material is in the state")
	}

	// read under always: the digest from sha256sum, the mode normalised
	read, err := s.Read(p.ReadRequest{ID: state.Path, Urn: protectedURN, Properties: created.Properties})
	if err != nil || read.Properties.Get("digest").AsString() != digestOf([]byte(secret)) || read.Properties.Get("mode").AsString() != "0600" {
		t.Fatalf("read %+v %v", read, err)
	}

	// diff under always asks the source: unchanged, then rotated
	same, err := s.Diff(p.DiffRequest{ID: state.Path, Urn: protectedURN, State: read.Properties, Inputs: protectedInputs(fileSource(src), "always")})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged diff %+v %v", same, err)
	}
	if err := os.WriteFile(src, []byte("rotated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.Diff(p.DiffRequest{ID: state.Path, Urn: protectedURN, State: read.Properties, Inputs: protectedInputs(fileSource(src), "always")})
	if err != nil || !rotated.HasChanges || !rotated.DeleteBeforeReplace {
		t.Fatalf("rotated diff %+v %v", rotated, err)
	}

	// under once nothing is fetched: a source that cannot be read is never touched
	missing := fileSource(filepath.Join(dir, "absent"))
	onceState := read.Properties.Set("enforce", property.New("once")).Set("digest", property.New(seeded))
	if d, err := s.Diff(p.DiffRequest{ID: state.Path, Urn: protectedURN, State: onceState, Inputs: protectedInputs(missing, "once")}); err != nil || d.HasChanges {
		t.Fatalf("once diff %+v %v", d, err)
	}
	onceRead, err := s.Read(p.ReadRequest{ID: state.Path, Urn: protectedURN, Properties: onceState})
	if err != nil || onceRead.Properties.Get("digest").AsString() != seeded {
		t.Fatalf("once read %+v %v", onceRead, err)
	}
	before := len(fake.Calls)
	updated, err := s.Update(p.UpdateRequest{ID: state.Path, Urn: protectedURN, State: onceState, Inputs: protectedInputs(missing, "once")})
	if err != nil || updated.Properties.Get("digest").AsString() != seeded {
		t.Fatalf("once update %+v %v", updated, err)
	}
	if got := fake.Commands()[before:]; len(got) != 1 || !strings.Contains(got[0], "chmod") || strings.Contains(got[0], "cat >") {
		t.Fatalf("once update ran %v", got)
	}

	if err := s.Delete(p.DeleteRequest{ID: state.Path, Urn: protectedURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
}

// A source that cannot work is refused before anything runs, naming the resource and the field.
func TestProtectedFileRefusesABadSourceFirst(t *testing.T) {
	fake := remotetest.New()
	s := configured(t, fake)
	bad := property.New(map[string]property.Value{"kind": property.New("execFile"), "argv": property.New([]property.Value{property.New("trove")})})
	_, err := s.Create(p.CreateRequest{Urn: protectedURN, Properties: protectedInputs(bad, "always")})
	if err == nil || !strings.Contains(err.Error(), "ProtectedFile: source.argv for kind 'execFile' has no '{}'") {
		t.Fatalf("got %v", err)
	}
	if len(fake.Calls) != 0 {
		t.Fatalf("ran %v", fake.Commands())
	}
}

// exec takes stdout; execFile takes what the command wrote to {}, and the temporary file is gone
// afterwards.
func TestCollectRunsTheSourceLocally(t *testing.T) {
	got, err := collect(t.Context(), Source{Kind: "exec", Argv: []string{"printf", "%s", "from stdout"}})
	if err != nil || string(got) != "from stdout" {
		t.Fatalf("exec: %q %v", got, err)
	}
	record := filepath.Join(t.TempDir(), "where")
	got, err = collect(t.Context(), Source{Kind: "execFile", Argv: []string{"sh", "-c", `printf written > "$1"; printf %s "$1" > "$2"`, "sh", "{}", record}})
	if err != nil || string(got) != "written" {
		t.Fatalf("execFile: %q %v", got, err)
	}
	where, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(string(where)); !os.IsNotExist(err) {
		t.Fatalf("the temporary file %s is still there: %v", where, err)
	}
	if _, err := collect(t.Context(), Source{Kind: "exec", Argv: []string{"sh", "-c", "echo nope >&2; exit 3"}}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("a failing source: %v", err)
	}
}

func TestProtectedFileImport(t *testing.T) {
	const path = "/home/chris/.ssh/id_ed25519"
	dir := t.TempDir()
	// a source that cannot be read proves nothing is fetched outside a diff under always
	unreadable := fileSource(filepath.Join(dir, "absent"))
	fake := remotetest.New().
		On(asRoot(readProtectedCommand(path, "once")), remotetest.Reply{Stdout: "600 chris chris\n"}).
		On(asRoot(readProtectedCommand(path, "always")), remotetest.Reply{Stdout: digestOf([]byte("k")) + "\n600 chris chris\n"})
	s := configured(t, fake)

	imported, err := s.Read(p.ReadRequest{ID: path, Urn: protectedURN})
	if err != nil {
		t.Fatal(err)
	}
	if got := imported.Inputs.Get("path").AsString(); got != path {
		t.Fatalf("input path %q", got)
	}
	if _, ok := imported.Inputs.GetOk("enforce"); ok {
		t.Fatalf("import claimed an enforcement: %v", imported.Inputs)
	}
	if got := imported.Properties.Get("digest").AsString(); got != digestOf([]byte("k")) {
		t.Fatalf("digest %q", got)
	}
	// the program that declares it diffs clean: under once without fetching anything, under always
	// against what the source holds
	if d, err := s.Diff(p.DiffRequest{ID: path, Urn: protectedURN, State: imported.Properties, Inputs: protectedInputs(unreadable, "once")}); err != nil || d.HasChanges {
		t.Fatalf("diff under once after import %+v %v", d, err)
	}
	src := filepath.Join(dir, "k")
	if err := os.WriteFile(src, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: path, Urn: protectedURN, State: imported.Properties, Inputs: protectedInputs(fileSource(src), "always")}); err != nil || d.HasChanges {
		t.Fatalf("diff under always after import %+v %v", d, err)
	}
	if err := os.WriteFile(src, []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: path, Urn: protectedURN, State: imported.Properties, Inputs: protectedInputs(fileSource(src), "always")}); err != nil || !d.HasChanges {
		t.Fatalf("a different source after import was no change %+v %v", d, err)
	}

	// a refresh after the import takes the enforcement from the recorded inputs
	refreshed, err := s.Read(p.ReadRequest{ID: path, Urn: protectedURN, Properties: imported.Properties, Inputs: protectedInputs(unreadable, "once")})
	if err != nil || refreshed.Properties.Get("enforce").AsString() != "once" || refreshed.Properties.Get("digest").AsString() != seeded {
		t.Fatalf("refresh after import %+v %v", refreshed, err)
	}

	if !refreshed.Inputs.Get("source").Equals(unreadable) {
		t.Fatalf("refresh lost declared source: %v", refreshed.Inputs)
	}
}
