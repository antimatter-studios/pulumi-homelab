package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var fileURN = presource.URN("urn:pulumi:s::p::homelab:index:ManagedFile::page")

func fileInputs(path, content string) property.Map {
	return property.NewMap(map[string]property.Value{
		"path": property.New(path), "content": property.New(content), "mode": property.New("0640"),
		"owner": property.New("www-data"), "group": property.New("www-data"),
	})
}

// fakeDisk is a fake machine holding one file, which answers the read and write commands the way
// the shell would: the write takes its body from stdin.
type fakeDisk struct {
	path, content, stat string
	exists              bool
	writes              int
}

func (d *fakeDisk) runner() *remotetest.Runner {
	fake := remotetest.New()
	fake.When(func(c string) bool { return c == asRoot(readFileCommand(d.path)) }, func(string) remotetest.Reply {
		if !d.exists {
			return remotetest.Reply{Code: 9}
		}
		return remotetest.Reply{Stdout: d.stat + "\n" + d.content}
	}, 0)
	fake.When(func(c string) bool { return c == "sudo -n sh -c "+shellQuote("rm -f "+shellQuote(d.path)) }, func(string) remotetest.Reply {
		d.exists = false
		return remotetest.Reply{}
	}, 0)
	return fake
}

// write scripts the write command for w on fake, recording the body sent on stdin.
func (d *fakeDisk) write(fake *remotetest.Runner, w wantedFile) {
	fake.When(func(c string) bool { return c == asRoot(writeFileCommand(w)) }, func(string) remotetest.Reply {
		d.exists, d.writes = true, d.writes+1
		d.content = fake.Calls[len(fake.Calls)-1].Stdin
		d.stat = strings.TrimPrefix(w.mode, "0") + " " + w.owner + " " + w.group
		return remotetest.Reply{}
	}, 0)
}

func pageFile(content string) wantedFile {
	return wantedFile{path: "/srv/www/page.html", content: content, mode: "0640", owner: "www-data", group: "www-data"}
}

// Ported from src/resources/file.test.ts "ends a big file with a newline, as a small one does" and
// src/ssh.test.ts "writing a file through a here-document": the newline rule the here-document
// imposed stays, or every file diffs on the switch.
func TestWriteAddsTrailingNewline(t *testing.T) {
	t.Run("ends a big file with a newline, as a small one does", func(t *testing.T) {
		big := strings.Repeat("x", 100_000)
		if got := fileBody(big); got != big+"\n" {
			t.Fatalf("big file: got %d bytes ending %q", len(got), got[len(got)-3:])
		}
		if got := fileBody("x"); got != "x\n" {
			t.Fatalf("small file: got %q", got)
		}
	})
	t.Run("ends the body with a newline, or the closing delimiter is not on its own line", func(t *testing.T) {
		if got := fileBody("a=b"); got != "a=b\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("does not add a second newline to a body that already has one", func(t *testing.T) {
		if got := fileBody("a=b\n"); got != "a=b\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("what goes over the transport is the body with its newline", func(t *testing.T) {
		disk := &fakeDisk{path: "/srv/www/page.html"}
		fake := disk.runner()
		disk.write(fake, pageFile("x"))
		s := configured(t, fake)
		if _, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: fileInputs("/srv/www/page.html", "x")}); err != nil {
			t.Fatal(err)
		}
		if disk.content != "x\n" {
			t.Fatalf("stdin was %q", disk.content)
		}
	})
}

// Ported from src/resources/file.test.ts "arrives whole, in commands that each fit". The second
// half is obsolete: the content goes as stdin data, so there is no command-line limit to fit. What
// remains is that more than 1 MB arrives byte-exact, in one command that does not contain it.
func TestWriteLargeFile(t *testing.T) {
	content := strings.Repeat("<p>'quotes' $HOME \\ é 🇪🇸</p>\n", 30_000)
	if len(content) <= 1<<20 {
		t.Fatalf("test content is only %d bytes", len(content))
	}
	disk := &fakeDisk{path: "/srv/www/page.html"}
	fake := disk.runner()
	disk.write(fake, pageFile(content))
	s := configured(t, fake)
	created, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: fileInputs("/srv/www/page.html", content)})
	if err != nil {
		t.Fatal(err)
	}
	if disk.content != content {
		t.Fatalf("content arrived as %d bytes, want %d", len(disk.content), len(content))
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("wrote in %d commands: %v", len(fake.Calls), fake.Commands())
	}
	if c := fake.Calls[0].Command; len(c) > 1024 || strings.Contains(c, "quotes") {
		t.Fatalf("the content went into the command: %d bytes", len(c))
	}
	if got := created.Properties.Get("digest").AsString(); got != digestOf([]byte(content)) {
		t.Fatalf("digest %s", got)
	}
	if _, ok := created.Properties.GetOk("content"); ok {
		t.Fatal("the state holds a second copy of the content")
	}
}

func TestWriteFileCommand(t *testing.T) {
	w := pageFile(strings.Repeat("é", 32*1024))
	w.reloadSystemd = true
	wantDirect := "mkdir -p '/srv/www' && cat > '/srv/www/page.html'\n" +
		"chmod '0640' '/srv/www/page.html' && chown 'www-data:www-data' '/srv/www/page.html' && systemctl daemon-reload"
	if got := writeFileCommand(w); got != wantDirect {
		t.Fatalf("direct write got %q, want %q", got, wantDirect)
	}
	w.content += "x"
	wantStaged := `mkdir -p '/srv/www' && staged=$(mktemp -- '/srv/www/.pulumi-file.XXXXXX') &&
trap 'rm -f -- "$staged"' EXIT &&
cat > "$staged" && chmod '0640' "$staged" && chown 'www-data:www-data' "$staged" && mv -fT -- "$staged" '/srv/www/page.html' && systemctl daemon-reload`
	if got := writeFileCommand(w); got != wantStaged {
		t.Fatalf("staged write got %q, want %q", got, wantStaged)
	}
	for path, parent := range map[string]string{"/etc/x.conf": "/etc", "/x": "/", "x": "x", "/a/b/": "/a/b", "": "/"} {
		if got := parentOf(path); got != parent {
			t.Errorf("parentOf(%q) = %q, want %q", path, got, parent)
		}
	}
}

// Create, read, diff, update and delete through the gRPC surface, with the machine faked.
func TestManagedFileLifecycle(t *testing.T) {
	const path = "/srv/www/page.html"
	disk := &fakeDisk{path: path}
	fake := disk.runner()
	disk.write(fake, pageFile("one"))
	disk.write(fake, pageFile("two\n"))
	s := configured(t, fake)

	created, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: fileInputs(path, "one")})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != path || created.Properties.Get("mode").AsString() != "0640" {
		t.Fatalf("created %s %v", created.ID, created.Properties)
	}

	if got := created.Properties.Get("digest").AsString(); got != digestOf([]byte("one\n")) {
		t.Fatalf("create digest %s", got)
	}

	// read: the stat mode 640 comes back as 0640, and the digest of what is on disk matches
	read, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN, Properties: created.Properties, Inputs: fileInputs(path, "one")})
	if err != nil || read.ID != path || read.Properties.Get("mode").AsString() != "0640" ||
		read.Properties.Get("digest").AsString() != digestOf([]byte("one\n")) {
		t.Fatalf("read %+v %v", read, err)
	}

	same, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: read.Properties, Inputs: fileInputs(path, "one")})
	if err != nil || same.HasChanges {
		t.Fatalf("unchanged content diffs after refresh %+v %v", same, err)
	}
	edited, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: read.Properties, Inputs: fileInputs(path, "two\n")})
	if err != nil || !edited.HasChanges || edited.DetailedDiff["content"].Kind != p.Update || edited.DeleteBeforeReplace {
		t.Fatalf("content diff %+v %v", edited, err)
	}
	moved, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: read.Properties, Inputs: fileInputs("/srv/www/other.html", "one")})
	if err != nil || moved.DetailedDiff["path"].Kind != p.UpdateReplace || moved.DeleteBeforeReplace {
		t.Fatalf("path diff %+v %v", moved, err)
	}
	reload := fileInputs(path, "one").Set("reloadSystemd", property.New(true))
	if r, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: read.Properties, Inputs: reload}); err != nil || r.DetailedDiff["reloadSystemd"].Kind != p.Update {
		t.Fatalf("reloadSystemd diff %+v %v", r, err)
	}

	// Removing just the final newline is also drift.
	disk.content = "one"
	drifted, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN, Properties: read.Properties, Inputs: fileInputs(path, "one")})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: drifted.Properties, Inputs: fileInputs(path, "one")}); err != nil || !d.HasChanges || d.DetailedDiff["content"].Kind != p.Update {
		t.Fatalf("missing newline did not report content drift: %+v %v", d, err)
	}

	// update: writes when different, and does nothing when the machine already matches
	if _, err := s.Update(p.UpdateRequest{ID: path, Urn: fileURN, State: drifted.Properties, Inputs: fileInputs(path, "two\n")}); err != nil {
		t.Fatal(err)
	}
	if disk.content != "two\n" || disk.writes != 2 {
		t.Fatalf("update wrote %q, %d writes", disk.content, disk.writes)
	}
	if _, err := s.Update(p.UpdateRequest{ID: path, Urn: fileURN, State: drifted.Properties, Inputs: fileInputs(path, "two\n")}); err != nil {
		t.Fatal(err)
	}
	if disk.writes != 2 {
		t.Fatal("an update that changed nothing rewrote the file")
	}

	// import: no inputs, so they come from the file as it is, except the content, which may be a
	// secret; the digest stands for it, and a declaration of the same content diffs clean
	imported, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN})
	if err != nil || imported.Inputs.Get("content").AsString() != "" || imported.Inputs.Get("owner").AsString() != "www-data" ||
		imported.Properties.Get("digest").AsString() != digestOf([]byte("two\n")) {
		t.Fatalf("import %+v %v", imported, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: imported.Properties, Inputs: fileInputs(path, "two\n")}); err != nil || diff.HasChanges {
		t.Fatalf("an import of the declared content diffs %+v %v", diff, err)
	}

	// delete removes the file; a read afterwards forgets the resource
	if err := s.Delete(p.DeleteRequest{ID: path, Urn: fileURN, Properties: drifted.Properties}); err != nil {
		t.Fatal(err)
	}
	gone, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN, Properties: drifted.Properties})
	if err != nil || gone.ID != "" {
		t.Fatalf("read of a deleted file %+v %v", gone, err)
	}
}

func TestManagedFileLegacyDigestUpdateSkipsWrite(t *testing.T) {
	const path = "/srv/www/page.html"
	const content = "one"
	disk := &fakeDisk{path: path, content: content + "\n", stat: "640 www-data www-data", exists: true}
	fake := disk.runner()
	w := pageFile(content)
	w.reloadSystemd = true
	disk.write(fake, w)
	s := configured(t, fake)

	inputs := fileInputs(path, content).Set("reloadSystemd", property.New(true))
	legacyState := property.NewMap(map[string]property.Value{
		"path": property.New(path), "mode": property.New(w.mode),
		"owner": property.New(w.owner), "group": property.New(w.group),
		"reloadSystemd": property.New(true), "digest": property.New(digestOf([]byte(content))),
	})

	diff, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: legacyState, OldInputs: inputs, Inputs: inputs})
	if err != nil || !diff.HasChanges || len(diff.DetailedDiff) != 1 || diff.DetailedDiff["content"].Kind != p.Update {
		t.Fatalf("legacy digest diff: %+v %v", diff, err)
	}
	updated, err := s.Update(p.UpdateRequest{ID: path, Urn: fileURN, State: legacyState, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if disk.writes != 0 || len(fake.Calls) != 1 || fake.Calls[0].Command != asRoot(readFileCommand(path)) {
		t.Fatalf("updating the digest should only read the file, got %v", fake.Commands())
	}
	if got, want := updated.Properties.Get("digest").AsString(), digestOf([]byte(content+"\n")); got != want {
		t.Fatalf("updated digest %s, want %s", got, want)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: updated.Properties, OldInputs: inputs, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after updating the digest: %+v %v", diff, err)
	}
}

// With sudo -S the password and the content cannot share one stdin: sudo reads the password only
// when it asks for one, and otherwise the password would land in the file. The content is staged
// unescalated first, with nothing but the content on that stream.
func TestSudoPasswordNeverMixesThePasswordIntoTheContent(t *testing.T) {
	w := pageFile("secret-free content")
	fake := remotetest.New().
		On(`umask 077 && t=$(mktemp) && cat > "$t" && printf %s "$t"`, remotetest.Reply{Stdout: "/tmp/tmp.abc"})
	escalated := escalate("sudo-password", `t='/tmp/tmp.abc'; trap 'rm -f -- "$t"' EXIT; (`+writeFileCommand(w)+`) < "$t"`)
	fake.On(escalated, remotetest.Reply{})
	s := newServer(t, (&capture{runner: fake}).factory)
	cfg := with(with(homelabServerConfig(), "escalation", property.New("sudo-password")), "sudoPassword", property.New("pw").WithSecret(true))
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(cfg)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: fileInputs(w.path, w.content)}); err != nil {
		t.Fatal(err)
	}
	if len(fake.Calls) != 2 || fake.Calls[0].Stdin != "secret-free content\n" || fake.Calls[1].Stdin != "pw\n" {
		t.Fatalf("calls %+v", fake.Calls)
	}
}

// The #38 switch adopts each existing file with `import:`. The engine's Read for an import has no
// inputs and no state, so it cannot know reloadSystemd, a behaviour rather than a fact the file
// shows; the program's declaration must still diff clean against it, or the import is refused.
func TestManagedFileImportIgnoresReloadSystemd(t *testing.T) {
	const path = "/srv/www/page.html"
	disk := &fakeDisk{path: path}
	fake := disk.runner()
	disk.write(fake, pageFile("one"))
	s := configured(t, fake)
	if _, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: fileInputs(path, "one\n")}); err != nil {
		t.Fatal(err)
	}

	imported, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN})
	if err != nil || imported.ID != path {
		t.Fatalf("import read %+v %v", imported, err)
	}
	if v, ok := imported.Properties.GetOk("reloadSystemd"); ok && !v.IsNull() {
		t.Fatalf("an import claimed to know reloadSystemd: %v", v)
	}
	for _, reload := range []bool{true, false} {
		declared := fileInputs(path, "one\n").Set("reloadSystemd", property.New(reload))
		d, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: imported.Properties, Inputs: declared})
		if err != nil || d.HasChanges {
			t.Fatalf("reloadSystemd %v: import diff %+v %v", reload, d, err)
		}
	}
}
