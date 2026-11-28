package homelab

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/archive"
)

var archiveSrc = archiveSource{
	URL:    "https://example.com/widget/v1.4.2/widget-linux-arm64.tar.gz",
	Sha256: strings.Repeat("a", 64),
	Prefix: "/opt/widget/1.4.2",
	Link:   "/opt/widget/current",
	Strip:  1,
	Format: archive.TarGz,
}

func installScript(t *testing.T, src archiveSource, owner, group string) string {
	t.Helper()
	if owner == "" {
		owner = archiveDefaultOwner
	}
	if group == "" {
		group = archiveDefaultGroup
	}
	s, err := archiveInstallScript(src, owner, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func extract(t *testing.T, format archive.Format, file, into string, strip int) string {
	t.Helper()
	c, err := extractCommand(format, file, into, strip)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func resolvedArchiveFor(t *testing.T, args ArchiveArgs) resolvedArchive {
	t.Helper()
	r, err := resolveArchiveArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func containsAll(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(text, part) {
			t.Errorf("%q does not contain %q", text, part)
		}
	}
}

func lacks(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if strings.Contains(text, part) {
			t.Errorf("%q contains %q", text, part)
		}
	}
}

func before(t *testing.T, text, first, second string) {
	t.Helper()
	i, j := strings.Index(text, first), strings.Index(text, second)
	if i < 0 || j < 0 || i >= j {
		t.Errorf("%q does not come before %q in %q", first, second, text)
	}
}

// Ported from src/resources/archive.test.ts: unpacking.
func TestExtractCommand(t *testing.T) {
	t.Run("uses the decompressor that matches the format", func(t *testing.T) {
		containsAll(t, extract(t, archive.TarGz, "/t/a", "/t/u", 0), "tar -z")
		containsAll(t, extract(t, archive.TarXz, "/t/a", "/t/u", 0), "tar -J")
		containsAll(t, extract(t, archive.TarBz2, "/t/a", "/t/u", 0), "tar -j")
		containsAll(t, extract(t, archive.Zip, "/t/a", "/t/u", 0), "unzip")
	})
	t.Run("strips only when asked to", func(t *testing.T) {
		lacks(t, extract(t, archive.TarGz, "/t/a", "/t/u", 0), "--strip-components")
		containsAll(t, extract(t, archive.TarGz, "/t/a", "/t/u", 1), "--strip-components=1")
	})
	t.Run("refuses a strip on a zip rather than ignoring one", func(t *testing.T) {
		_, err := extractCommand(archive.Zip, "/t/a", "/t/u", 1)
		if err == nil || !strings.Contains(err.Error(), "strip-components") {
			t.Fatalf("got %v", err)
		}
		if err.Error() != "unzip has no --strip-components, so strip: 1 cannot be honoured for a zip" {
			t.Fatalf("got %q", err)
		}
	})
	t.Run("quotes both paths", func(t *testing.T) {
		containsAll(t, extract(t, archive.TarGz, "/t/it's", "/t/u", 0), `'/t/it'\''s'`)
	})
}

// Ported from src/resources/archive.test.ts: composing the install.
func TestArchiveInstallScript(t *testing.T) {
	script := installScript(t, archiveSrc, "", "")
	t.Run("verifies before it unpacks", func(t *testing.T) {
		before(t, script, "sha256sum -c", "tar ")
	})
	t.Run("stops the whole command when verification fails", func(t *testing.T) {
		if !strings.HasPrefix(script, "set -e") {
			t.Fatal(script)
		}
	})
	t.Run("downloads into a private directory rather than a predictable path", func(t *testing.T) {
		containsAll(t, script, "mktemp -d")
		if regexp.MustCompile(`-o ['"]?/tmp/[a-z]`).MatchString(script) {
			t.Fatal(script)
		}
	})
	t.Run("removes the download whether or not it was accepted", func(t *testing.T) {
		containsAll(t, script, "trap")
	})
	t.Run("unpacks to a staging directory and moves it, so a half-extracted tree is never visible", func(t *testing.T) {
		before(t, script, `-C "$dir/unpacked"`, `mv -nT -- "$dir/unpacked"`)
		lacks(t, script, "-C '/opt/widget/1.4.2'")
	})
	t.Run("moves the link last, because that is the step anything else can see", func(t *testing.T) {
		before(t, script, `mv -nT -- "$dir/unpacked"`, "ln -sfn")
	})
	t.Run("relinks with -n, so a link at a directory is replaced rather than written inside it", func(t *testing.T) {
		containsAll(t, script, "ln -sfn")
	})
	t.Run("leaves the link step out when there is no link", func(t *testing.T) {
		noLink := archiveSrc
		noLink.Link = ""
		lacks(t, installScript(t, noLink, "", ""), "ln -s")
		// an undeclared link settles to "" too
		r := resolvedArchiveFor(t, ArchiveArgs{Name: "w", URL: &archiveSrc.URL, Sha256: strp("a"), Prefix: "/opt/w"})
		lacks(t, installScript(t, r.archiveSource, r.Owner, r.Group), "ln -s")
	})
	t.Run("passes the staging paths as shell variables rather than as quoted literals", func(t *testing.T) {
		containsAll(t, script, `-C "$dir/unpacked"`)
		lacks(t, script, "'$dir")
	})
	t.Run("quotes the url, so a query string cannot become shell syntax", func(t *testing.T) {
		src := archiveSrc
		src.URL = "https://example.com/a.tar.gz?x=1&y=2"
		s := installScript(t, src, "", "")
		containsAll(t, s, "'https://example.com/a.tar.gz?x=1&y=2'")
		if regexp.MustCompile(`curl [^']*&y=2`).MatchString(s) {
			t.Fatal(s)
		}
	})
	t.Run("quotes the checksum and the prefix", func(t *testing.T) {
		src := archiveSrc
		src.Prefix = "/opt/it's"
		containsAll(t, installScript(t, src, "", ""), "'"+strings.Repeat("a", 64)+"'", `'/opt/it'\''s'`)
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		if installScript(t, archiveSrc, "", "") != installScript(t, archiveSrc, "", "") {
			t.Fatal("differs")
		}
	})
	t.Run("checks an existing prefix before moving it", func(t *testing.T) {
		before(t, script, `check_tree "$prefix"`, `mv -T -- "$prefix" "$dir/previous"`)
		before(t, script, `check_tree "$dir/previous"`, `rm -rf --one-file-system -- "$dir/previous"`)
	})
}

// Ported from src/resources/archive.test.ts: where the install is looked for.
func TestWhereOf(t *testing.T) {
	t.Run("prefers the stable link, which is what outlives an upgrade", func(t *testing.T) {
		if got := whereOf("/opt/w/1.4.2", "/opt/w/current"); got != "/opt/w/current" {
			t.Fatal(got)
		}
	})
	t.Run("falls back to the prefix when there is no link", func(t *testing.T) {
		if got := whereOf("/opt/w/1.4.2", ""); got != "/opt/w/1.4.2" {
			t.Fatal(got)
		}
	})
	t.Run("takes a relative binary from there and leaves an absolute one alone", func(t *testing.T) {
		for _, c := range []struct{ where, binary, want string }{
			{"/opt/w/current", "bin/widget", "/opt/w/current/bin/widget"},
			{"/opt/w/current", "./bin/widget", "/opt/w/current/bin/widget"},
			{"/opt/w/current/", "bin/widget", "/opt/w/current/bin/widget"},
			{"/opt/w/current", "/usr/local/bin/widget", "/usr/local/bin/widget"},
			{"/opt/w/current", "", ""},
		} {
			if got := binaryPath(c.where, c.binary); got != c.want {
				t.Errorf("binaryPath(%q, %q) = %q, want %q", c.where, c.binary, got, c.want)
			}
		}
	})
}

// Ported from src/resources/archive.test.ts: asking the machine whether it is installed.
func TestProbe(t *testing.T) {
	const w = "/opt/w/current"
	t.Run("answers on the directory alone when nothing stronger was asked for", func(t *testing.T) {
		c := archiveProbeCommand(w, w, "", "", "")
		containsAll(t, c, "test -d '/opt/w/current'")
		lacks(t, c, "test -x")
	})
	t.Run("requires the binary to be executable when one was named", func(t *testing.T) {
		containsAll(t, archiveProbeCommand(w, w, "/opt/w/current/bin/widget", "", ""), "test -x '/opt/w/current/bin/widget'")
	})
	t.Run("requires the thing to run when a health command was given", func(t *testing.T) {
		containsAll(t, archiveProbeCommand(w, w, "", "{}/bin/widget --version", ""), "/opt/w/current/bin/widget --version")
	})
	t.Run("discards what the health command printed, because only the exit code is the answer", func(t *testing.T) {
		containsAll(t, archiveProbeCommand(w, w, "", "{}/bin/widget --version", ""), ">/dev/null 2>&1")
	})
	t.Run("requires every rung that was asked for, not any of them", func(t *testing.T) {
		c := archiveProbeCommand(w, w, "/opt/w/current/bin/widget", "{}/bin/widget --version", "")
		containsAll(t, c, "test -d", "test -x", "--version")
		lacks(t, c, "||  ")
		if n := len(strings.Split(c, "&&")); n <= 3 {
			t.Fatalf("%d parts in %q", n, c)
		}
	})
	t.Run("never fails the command over an absent install, because absence is an answer", func(t *testing.T) {
		containsAll(t, archiveProbeCommand(w, w, "", "", ""), "|| true")
	})
	t.Run("substitutes the install path into both commands, everywhere it appears", func(t *testing.T) {
		c := archiveProbeCommand(w, w, "", "{}/bin/widget check", "{}/bin/widget --version # {}")
		containsAll(t, c, "/opt/w/current/bin/widget check", "/opt/w/current/bin/widget --version")
		lacks(t, c, "{}")
	})
	t.Run("asks for the version in the same round trip, and never over ssh twice", func(t *testing.T) {
		before(t, archiveProbeCommand(w, w, "", "", "{}/bin/widget --version"), "test -d", "--version")
	})
	t.Run("leaves the version out entirely when nothing was given to ask with", func(t *testing.T) {
		if c := archiveProbeCommand(w, w, "", "", ""); !strings.HasSuffix(c, "#pulumi-homelab#version'") {
			t.Fatal(c)
		}
	})
	t.Run("asks who owns the prefix, not who owns the link", func(t *testing.T) {
		c := archiveProbeCommand(w, "/opt/w/1.4.2", "", "", "")
		containsAll(t, c, "stat -c '%U:%G' '/opt/w/1.4.2'")
		lacks(t, c, "stat -c '%U:%G' '/opt/w/current'")
	})
	t.Run("never fails over a prefix it cannot stat", func(t *testing.T) {
		containsAll(t, archiveProbeCommand("/opt/w", "/opt/w", "", "", ""), "2>/dev/null || true")
	})
	t.Run("does not let a version command that exits non-zero look like absence", func(t *testing.T) {
		out := "present\n#pulumi-homelab#owner\nroot:root\n#pulumi-homelab#version\nExec format error\n"
		got := parseProbe(out, archive.DefaultVersionPattern)
		if !got.Installed || got.Version != "" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("reads both halves back apart", func(t *testing.T) {
		out := "present\n#pulumi-homelab#owner\nsvc:svc\n#pulumi-homelab#version\nwidget 1.4.2 (abc1234)\n"
		if got, want := parseProbe(out, archive.DefaultVersionPattern), (probe{Installed: true, Owner: "svc", Group: "svc", Version: "1.4.2"}); got != want {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("reports absent when the test printed nothing", func(t *testing.T) {
		if got := parseProbe("\n#pulumi-homelab#owner\n\n#pulumi-homelab#version\n", archive.DefaultVersionPattern); got != (probe{}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("is exactly the TypeScript command", func(t *testing.T) {
		want := `{ test -d '/opt/w/current' && test -x '/opt/w/current/bin/w' && { /opt/w/current/bin/w ok ; } >/dev/null 2>&1 && echo present || true; }; ` +
			`echo '#pulumi-homelab#owner'; stat -c '%U:%G' '/opt/w/1' 2>/dev/null || true; echo '#pulumi-homelab#version'; /opt/w/current/bin/w -v 2>&1 || true`
		if got := archiveProbeCommand(w, "/opt/w/1", "/opt/w/current/bin/w", "{}/bin/w ok", "{}/bin/w -v"); got != want {
			t.Fatalf("got\n%s\nwant\n%s", got, want)
		}
	})
}

// Ported from src/resources/archive.test.ts: standing in for where the install ended up.
func TestSubstitute(t *testing.T) {
	t.Run("replaces every occurrence, because a pipeline names the path more than once", func(t *testing.T) {
		if got := substitute(`{}/bin/w --version | grep -q "$({}/bin/w id)"`, "/opt/w"); got != `/opt/w/bin/w --version | grep -q "$(/opt/w/bin/w id)"` {
			t.Fatal(got)
		}
	})
	t.Run("leaves a command that names no path alone", func(t *testing.T) {
		if got := substitute("widget --version", "/opt/w"); got != "widget --version" {
			t.Fatal(got)
		}
	})
}

// Ported from src/resources/archive.test.ts: who owns the unpacked tree.
func TestOwnership(t *testing.T) {
	base := ArchiveArgs{Name: "w", URL: strp("https://x/a.tar.gz"), Sha256: strp("a"), Prefix: "/opt/w"}
	withOwner := func(owner, group string) ArchiveArgs {
		a := base
		a.Owner, a.Group = &owner, &group
		return a
	}
	t.Run("chowns recursively, because an updater rewrites files inside the tree", func(t *testing.T) {
		if got := chownCommand("/home/svc/.local/w", "svc", "svc"); got != "chown -R 'svc:svc' '/home/svc/.local/w'" {
			t.Fatal(got)
		}
	})
	t.Run("chowns the staged tree before it is published", func(t *testing.T) {
		s := installScript(t, archiveSrc, "svc", "svc")
		before(t, s, "chown -R", `mv -nT -- "$dir/unpacked"`)
		before(t, s, "chown -R", "ln -sfn")
	})
	t.Run("chowns the link itself rather than what it points at", func(t *testing.T) {
		containsAll(t, installScript(t, archiveSrc, "svc", "svc"), "chown -h 'svc:svc' '/opt/widget/current'")
	})
	t.Run("defaults to root, so nothing declared before the field existed changes", func(t *testing.T) {
		containsAll(t, installScript(t, archiveSrc, "", ""), "chown -R -- 'root:root'")
	})
	t.Run("does not let the archive choose who owns files on the machine", func(t *testing.T) {
		containsAll(t, extract(t, archive.TarGz, "/t/a", "/t/u", 0), "--no-same-owner")
		containsAll(t, installScript(t, archiveSrc, "", ""), "--no-same-owner")
	})
	t.Run("reports a prefix owned by somebody else as changed", func(t *testing.T) {
		if !ownershipChanged("root", "root", "svc", "svc") || !ownershipChanged("svc", "root", "svc", "svc") || ownershipChanged("svc", "svc", "svc", "svc") {
			t.Fatal("wrong")
		}
	})
	t.Run("says nothing about a prefix that is not there", func(t *testing.T) {
		if ownershipChanged("", "", "svc", "svc") {
			t.Fatal("changed")
		}
	})
	t.Run("is not part of the source, so a changed owner does not refetch the archive", func(t *testing.T) {
		asRoot, asService := resolvedArchiveFor(t, base), resolvedArchiveFor(t, withOwner("svc", "svc"))
		if sourceChanged(asRoot.archiveSource, asService.archiveSource) {
			t.Fatal("source changed")
		}
		if installNeeded(true, &asRoot.archiveSource, asService.archiveSource) {
			t.Fatal("install needed")
		}
		if !ownershipChanged(asRoot.Owner, asRoot.Group, asService.Owner, asService.Group) {
			t.Fatal("ownership not changed")
		}
	})
	t.Run("chowns rather than fetching when only the owner is wrong", func(t *testing.T) {
		previous := resolvedArchiveFor(t, base).archiveSource
		if got := archiveAct(probe{Installed: true, Owner: "root", Group: "root"}, &previous, resolvedArchiveFor(t, withOwner("svc", "svc"))); got != actChown {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("still fetches for an absent install, whoever is meant to own it", func(t *testing.T) {
		owner := "svc"
		a := base
		a.Owner = &owner
		r := resolvedArchiveFor(t, a)
		if !installNeeded(false, &r.archiveSource, r.archiveSource) {
			t.Fatal("not needed")
		}
	})
	t.Run("settles to root when nothing was declared", func(t *testing.T) {
		if r := resolvedArchiveFor(t, base); r.Owner != "root" || r.Group != "root" {
			t.Fatalf("got %s:%s", r.Owner, r.Group)
		}
	})
	t.Run("carries a declared owner through", func(t *testing.T) {
		if r := resolvedArchiveFor(t, withOwner("svc", "agents")); r.Owner != "svc" || r.Group != "agents" {
			t.Fatalf("got %s:%s", r.Owner, r.Group)
		}
	})
}

// Ported from src/resources/archive.test.ts: deciding what an install needs.
func TestArchiveAct(t *testing.T) {
	owner, group := "svc", "svc"
	args := ArchiveArgs{Name: "w", URL: strp("https://x/a.tar.gz"), Sha256: strp("a"), Prefix: "/opt/w", Owner: &owner, Group: &group}
	wanted := resolvedArchiveFor(t, args)
	right := probe{Installed: true, Owner: "svc", Group: "svc"}
	absent := probe{}
	t.Run("does nothing to an install that is there and owned correctly", func(t *testing.T) {
		if got := archiveAct(right, &wanted.archiveSource, wanted); got != actNothing {
			t.Fatalf("got %s", got)
		}
		if got := archiveAct(right, nil, wanted); got != actNothing {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("installs what is absent, whatever it would be owned by", func(t *testing.T) {
		if got := archiveAct(absent, &wanted.archiveSource, wanted); got != actInstall {
			t.Fatalf("got %s", got)
		}
		if got := archiveAct(absent, nil, wanted); got != actInstall {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("installs when the source moved, and does not merely chown", func(t *testing.T) {
		moved := args
		moved.Sha256 = strp("b")
		previous := resolvedArchiveFor(t, moved).archiveSource
		if got := archiveAct(right, &previous, wanted); got != actInstall {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("prefers the chown when both could be said to apply", func(t *testing.T) {
		if got := archiveAct(probe{Installed: true, Owner: "root", Group: "root"}, &wanted.archiveSource, wanted); got != actChown {
			t.Fatalf("got %s", got)
		}
	})
}

// Ported from src/resources/archive.test.ts: deciding whether to fetch.
func TestInstallNeeded(t *testing.T) {
	src := archiveSrc
	moved := func(change func(*archiveSource)) archiveSource {
		s := archiveSrc
		change(&s)
		return s
	}
	t.Run("installs what is absent", func(t *testing.T) {
		if !installNeeded(false, nil, src) || !installNeeded(false, &src, src) {
			t.Fatal("not needed")
		}
	})
	t.Run("leaves alone what is already there and still described the same way", func(t *testing.T) {
		if installNeeded(true, &src, src) {
			t.Fatal("needed")
		}
	})
	t.Run("does not overwrite something already installed on a first apply", func(t *testing.T) {
		if installNeeded(true, nil, src) {
			t.Fatal("needed")
		}
	})
	t.Run("installs again when the url or the checksum move", func(t *testing.T) {
		if !installNeeded(true, &src, moved(func(s *archiveSource) { s.URL = "https://example.com/w/v2/w.tar.gz" })) {
			t.Fatal("url")
		}
		if !installNeeded(true, &src, moved(func(s *archiveSource) { s.Sha256 = strings.Repeat("b", 64) })) {
			t.Fatal("sha256")
		}
	})
	t.Run("installs again when where it goes or how it unpacks moves", func(t *testing.T) {
		for name, s := range map[string]archiveSource{
			"prefix": moved(func(s *archiveSource) { s.Prefix = "/opt/widget/2.0.0" }),
			"link":   moved(func(s *archiveSource) { s.Link = "/usr/local/widget" }),
			"strip":  moved(func(s *archiveSource) { s.Strip = 0 }),
			"format": moved(func(s *archiveSource) { s.Format = archive.TarXz }),
		} {
			if !sourceChanged(src, s) {
				t.Errorf("%s", name)
			}
		}
	})
	t.Run("has no version in it to change", func(t *testing.T) {
		for i := 0; i < reflect.TypeOf(src).NumField(); i++ {
			if name := reflect.TypeOf(src).Field(i).Name; strings.EqualFold(name, "version") {
				t.Fatal("the source has a version")
			}
		}
		if sourceChanged(src, src) {
			t.Fatal("changed")
		}
	})
}

// Ported from src/resources/archive.test.ts: settling the arguments.
func TestResolveArgs(t *testing.T) {
	base := ArchiveArgs{Name: "w", URL: strp("https://x/a.tar.gz"), Sha256: strp("a"), Prefix: "/opt/w"}
	t.Run("infers the format from the url", func(t *testing.T) {
		a := base
		a.URL = strp("https://x/a.tar.xz")
		if got := resolvedArchiveFor(t, a).Format; got != archive.TarXz {
			t.Fatal(got)
		}
	})
	t.Run("takes a given format over the url, for a download that carries no name", func(t *testing.T) {
		a := base
		a.URL, a.Format = strp("https://api.x/artifacts/1/download"), strp("zip")
		if got := resolvedArchiveFor(t, a).Format; got != archive.Zip {
			t.Fatal(got)
		}
	})
	t.Run("refuses a url it cannot read and names the way out", func(t *testing.T) {
		a := base
		a.URL = strp("https://api.x/artifacts/1")
		_, err := resolveArchiveArgs(a)
		if err == nil || !strings.Contains(err.Error(), "give format") {
			t.Fatalf("got %v", err)
		}
		if err.Error() != "cannot tell what kind of archive https://api.x/artifacts/1 is from its name — give format: 'tar.gz' | 'tar.xz' | 'tar.bz2' | 'zip'" {
			t.Fatalf("got %q", err)
		}
	})
	t.Run("resolves the binary against the link rather than the versioned prefix", func(t *testing.T) {
		a := base
		a.Prefix, a.Link, a.Binary = "/opt/w/1.4.2", strp("/opt/w/current"), strp("bin/widget")
		if got := resolvedArchiveFor(t, a).Binary; got != "/opt/w/current/bin/widget" {
			t.Fatal(got)
		}
	})
	t.Run("keeps the health command as given, since it is a command rather than a path", func(t *testing.T) {
		a := base
		a.HealthCommand = strp("{}/bin/w --version")
		if got := resolvedArchiveFor(t, a).HealthCommand; got != "{}/bin/w --version" {
			t.Fatal(got)
		}
	})
	t.Run("is the same answer every time, so nothing here can look like a change", func(t *testing.T) {
		if !reflect.DeepEqual(resolvedArchiveFor(t, base), resolvedArchiveFor(t, base)) {
			t.Fatal("differs")
		}
	})
}
