//go:build integration

package safeops

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real-shell tests in a Debian 13 container, as root. The container is privileged so tests can
// mount filesystems inside it, with a tmpfs at /srv/mnt. HOMELAB_SAFEOPS_TEST_HOOK runs a shell
// snippet at a named point of the script (pinned, captured, verified), with the pinned parent as
// its working directory, to change things mid-run.
type box struct{ id string }

func start(t *testing.T, opts ...string) box {
	t.Helper()
	args := append([]string{"run", "-d", "--quiet", "--rm", "--tmpfs", "/srv/mnt"}, opts...)
	args = append(args, "debian:trixie-slim", "sleep", "infinity")
	cmd := exec.Command("docker", args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output() // the container ID alone: pull diagnostics go to stderr
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, errb.String())
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return box{id}
}

func (b box) sh(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", b.id, "sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", command, err, out)
	}
	return string(out)
}

func (b box) run(t *testing.T, command, hook string) (int, string, string) {
	t.Helper()
	// the command goes in on stdin, which docker passes as raw bytes; arguments go through JSON,
	// which would replace a name that is not UTF-8
	cmd := exec.Command("docker", "exec", "-i", "-e", "HOMELAB_SAFEOPS_TEST_HOOK="+hook, b.id, "sh", "-s")
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(command), &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return code, out.String(), errb.String()
}

// install runs InstallCommand with content on stdin: the command itself goes in a file first.
func (b box) install(t *testing.T, p string, how InstallMode, mode, owner, group, content, hook string) Result {
	t.Helper()
	command, err := InstallCommand(p, how, mode, owner, group)
	if err != nil {
		t.Fatal(err)
	}
	put := exec.Command("docker", "exec", "-i", b.id, "sh", "-c", "cat > /root/install.sh")
	put.Stdin = strings.NewReader(command)
	if out, err := put.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	cmd := exec.Command("docker", "exec", "-i", "-e", "HOMELAB_SAFEOPS_TEST_HOOK="+hook, b.id, "sh", "/root/install.sh")
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(content), &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	r, err := Parse(code, out.String(), errb.String())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (b box) remove(t *testing.T, p string, want Expect, hook string) Result {
	t.Helper()
	command, err := Command(p, want)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(b.run(t, command, hook))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (b box) digest(t *testing.T, p string, kind Kind) string {
	t.Helper()
	command, err := DigestCommand(p, kind)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDigest(b.run(t, command, ""))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRealShell(t *testing.T) {
	b := start(t, "--privileged")
	b.sh(t, "mkdir -p /srv/a /srv/real && ln -s /srv/real /srv/p")
	noStaging := func(t *testing.T, dir string) {
		t.Helper()
		if got := b.sh(t, "ls -A "+dir+" | grep homelab-safeops || true"); got != "" {
			t.Fatalf("staging left in %s: %q", dir, got)
		}
	}
	const bad = "0000000000000000000000000000000000000000000000000000000000000000"

	t.Run("a matching symlink is removed", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/a/l")
		if r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Removed || r.Path != "" || r.Staging != "" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/l && test ! -L /srv/a/l")
		noStaging(t, "/srv/a")
	})
	t.Run("a mismatching symlink, or a file in its place, is restored untouched", func(t *testing.T) {
		b.sh(t, "ln -s /elsewhere /srv/a/l")
		if r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Restored || r.Reason != "it points somewhere else" {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "readlink /srv/a/l"); got != "/elsewhere\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm /srv/a/l && echo data > /srv/a/l")
		if r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Restored || r.Reason != "it is not a symlink" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, `test "$(cat /srv/a/l)" = data && rm /srv/a/l`)
		noStaging(t, "/srv/a")
	})
	t.Run("a symlink target with a trailing newline is compared exactly", func(t *testing.T) {
		b.sh(t, `perl -e 'symlink("/target\n", "/srv/a/n") or die'`)
		if r := b.remove(t, "/srv/a/n", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Restored {
			t.Fatalf("without the newline: %+v", r)
		}
		if r := b.remove(t, "/srv/a/n", Expect{Kind: Symlink, Value: "/target\n"}, ""); r.Outcome != Removed {
			t.Fatalf("with the newline: %+v", r)
		}
	})
	t.Run("a file is removed only when its digest matches", func(t *testing.T) {
		b.sh(t, "echo content > /srv/a/f")
		d := b.digest(t, "/srv/a/f", File)
		if sum := strings.Fields(b.sh(t, "sha256sum /srv/a/f"))[0]; d != sum {
			t.Fatalf("digest %s, sha256sum %s", d, sum)
		}
		if r := b.remove(t, "/srv/a/f", Expect{Kind: File, Value: bad}, ""); r.Outcome != Restored || r.Reason != "its content is not what was written" {
			t.Fatalf("%+v", r)
		}
		if r := b.remove(t, "/srv/a/f", Expect{Kind: File, Value: d}, ""); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/f")
	})
	t.Run("an empty or malformed expected digest is refused, nothing touched", func(t *testing.T) {
		b.sh(t, "echo content > /srv/a/f")
		for _, v := range []string{"", "  ", "xyz", strings.Repeat("A", 64), bad + "0"} {
			if r := b.remove(t, "/srv/a/f", Expect{Kind: File, Value: v}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "empty or malformed") {
				t.Fatalf("%q: %+v", v, r)
			}
		}
		if r := b.remove(t, "/srv/a/l2", Expect{Kind: Symlink, Value: ""}, ""); r.Outcome != Refused {
			t.Fatalf("empty target: %+v", r)
		}
		b.sh(t, `test "$(cat /srv/a/f)" = content`)
		noStaging(t, "/srv/a")
	})
	t.Run("a digest that fails or comes back empty is never a match", func(t *testing.T) {
		d := b.digest(t, "/srv/a/f", File)
		for hookless, fake := range map[string]string{
			"could not be computed": "#!/bin/sh\nexit 1\n",
			"came back empty":       "#!/bin/sh\ncat >/dev/null\n",
			"came back malformed":   "#!/bin/sh\ncat >/dev/null; echo nonsense\n",
		} {
			b.sh(t, "printf '"+strings.ReplaceAll(fake, "\n", `\n`)+"' > /usr/local/bin/sha256sum && chmod 755 /usr/local/bin/sha256sum")
			r := b.remove(t, "/srv/a/f", Expect{Kind: File, Value: d}, "")
			if r.Outcome != Restored || !strings.Contains(r.Reason, hookless) {
				t.Fatalf("%s: %+v", hookless, r)
			}
			b.sh(t, `test "$(cat /srv/a/f)" = content`)
		}
		b.sh(t, "rm /usr/local/bin/sha256sum /srv/a/f")
	})
	t.Run("a tree is digested without following links and removed only when it matches", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/t/sub /srv/outside && echo one > /srv/outside/x && echo f > /srv/a/t/sub/f && ln -s /srv/outside/x /srv/a/t/link")
		before := b.digest(t, "/srv/a/t", Tree)
		b.sh(t, "echo two > /srv/outside/x")
		if after := b.digest(t, "/srv/a/t", Tree); after != before {
			t.Fatal("the digest followed a symlink out of the tree")
		}
		b.sh(t, "ln -sfn /srv/outside/y /srv/a/t/link")
		if b.digest(t, "/srv/a/t", Tree) == before {
			t.Fatal("a changed link target did not change the digest")
		}
		b.sh(t, "ln -sfn /srv/outside/x /srv/a/t/link")
		if r := b.remove(t, "/srv/a/t", Expect{Kind: Tree, Value: bad}, ""); r.Outcome != Restored {
			t.Fatalf("%+v", r)
		}
		b.sh(t, `test "$(cat /srv/a/t/sub/f)" = f`)
		if r := b.remove(t, "/srv/a/t", Expect{Kind: Tree, Value: before}, ""); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, `test ! -e /srv/a/t && test "$(cat /srv/outside/x)" = two`)
		noStaging(t, "/srv/a")
	})
	t.Run("names that are not UTF-8 are digested and removed", func(t *testing.T) {
		b.sh(t, `mkdir "/srv/a/$(printf 'n\377')" && echo x > "/srv/a/$(printf 'n\377')/$(printf '\376f')"`)
		name := "/srv/a/n\xff"
		d := b.digest(t, name, Tree)
		if r := b.remove(t, name, Expect{Kind: Tree, Value: d}, ""); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, `test ! -e "/srv/a/$(printf 'n\377')"`)
	})
	t.Run("an empty directory is removed, one with contents is restored", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/e /srv/a/full && touch /srv/a/full/data")
		if r := b.remove(t, "/srv/a/full", Expect{Kind: EmptyDir}, ""); r.Outcome != Restored || r.Reason != "it is not empty" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -f /srv/a/full/data && rm -r /srv/a/full")
		if r := b.remove(t, "/srv/a/e", Expect{Kind: EmptyDir}, ""); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/e")
		noStaging(t, "/srv/a")
	})
	t.Run("a directory restore never replaces an empty directory that appeared at the name", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/full && touch /srv/a/full/data")
		r := b.remove(t, "/srv/a/full", Expect{Kind: EmptyDir}, `[ "$1" = verified ] && mkdir full`)
		if r.Outcome != Kept || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") || !strings.HasSuffix(r.Path, "/entry") || !strings.Contains(r.Reason, "File exists") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -d /srv/a/full && test -z \"$(ls -A /srv/a/full)\" && test -f "+r.Path+"/data")
		b.sh(t, "rm -r /srv/a/full /srv/a/.homelab-safeops.*")
	})
	t.Run("a link restore that would clobber something new keeps the entry at its exact path", func(t *testing.T) {
		b.sh(t, "ln -s /elsewhere /srv/a/l")
		r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Value: "/target"}, `[ "$1" = verified ] && echo new > l`)
		if r.Outcome != Kept || !strings.HasSuffix(r.Path, "/entry") || r.Staging == "" || r.ParentDevIno == "" || !strings.Contains(r.Reason, "File exists") {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/a/l; readlink "+r.Path); got != "new\n/elsewhere\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm -r /srv/a/l /srv/a/.homelab-safeops.*")
	})
	t.Run("a parent moved in flight is reported by staging name and dev:ino, not a stale path", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/mv && ln -s /elsewhere /srv/mv/l")
		devino := strings.TrimSpace(b.sh(t, "stat -c %d:%i /srv/mv"))
		r := b.remove(t, "/srv/mv/l", Expect{Kind: Symlink, Value: "/target"}, `[ "$1" = verified ] && echo new > l && mv "$PWD" /srv/moved && mkdir /srv/mv`)
		if r.Outcome != Kept || r.Path != "" || r.Parent != "/srv/mv" || r.ParentDevIno != devino || !strings.Contains(r.Reason, "the parent moved") {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "readlink /srv/moved/"+r.Staging+"/entry"); got != "/elsewhere\n" {
			t.Fatalf("%q", got)
		}
		if !strings.Contains(r.Warning("Symlink /srv/mv/l"), "staging directory "+r.Staging+" under the directory with dev:ino "+devino) {
			t.Fatal(r.Warning("Symlink /srv/mv/l"))
		}
		b.sh(t, "rm -r /srv/moved /srv/mv")
	})
	t.Run("a private directory that cannot be cleaned up is reported", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/a/l")
		r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Value: "/target"}, `[ "$1" = verified ] && for d in .homelab-safeops.*; do touch "$d/stray"; done`)
		if r.Outcome != Removed || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") || !strings.Contains(r.Reason, "Directory not empty") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -f "+r.Path+"/stray && rm -r "+r.Path)
	})
	t.Run("a removal that stops part way is reported as partial", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/t/sub && echo f > /srv/a/t/sub/f && echo g > /srv/a/t/g")
		d := b.digest(t, "/srv/a/t", Tree)
		r := b.remove(t, "/srv/a/t", Expect{Kind: Tree, Value: d}, `[ "$1" = verified ] && mount -t tmpfs none .homelab-safeops.*/entry/sub`)
		if r.Outcome != Partial || r.Remaining < 2 || !strings.Contains(r.Reason, "on another filesystem") || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "umount "+r.Path+"/entry/sub && rm -r "+r.Path)
	})
	t.Run("an entry on another filesystem, or with a mount under it, is refused", func(t *testing.T) {
		b.sh(t, "echo inside > /srv/mnt/f")
		if r := b.remove(t, "/srv/mnt", Expect{Kind: Tree, Value: bad}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "/srv/mnt is a mount point") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "mkdir -p /srv/a/t/m /srv/a/t2/b /srv/x && mount -t tmpfs none /srv/a/t/m && mount --bind /srv/x /srv/a/t2/b")
		if r := b.remove(t, "/srv/a/t", Expect{Kind: Tree, Value: bad}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "/srv/a/t/m is a mount point") {
			t.Fatalf("nested mount: %+v", r)
		}
		if r := b.remove(t, "/srv/a/t2", Expect{Kind: Tree, Value: bad}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "/srv/a/t2/b is a mount point") {
			t.Fatalf("same-device bind: %+v", r)
		}
		b.sh(t, `test "$(cat /srv/mnt/f)" = inside && test -d /srv/a/t/m && test -d /srv/a/t2/b && umount /srv/a/t/m /srv/a/t2/b && rm -r /srv/a/t /srv/a/t2`)
		noStaging(t, "/srv")
		noStaging(t, "/srv/a")
	})
	t.Run("a parent that is a symlink, under one, or writable without the sticky bit is refused", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/real/l && mkdir /srv/real/sub && ln -s /target /srv/real/sub/l")
		if r := b.remove(t, "/srv/p/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "the parent /srv/p is a symlink") {
			t.Fatalf("%+v", r)
		}
		if r := b.remove(t, "/srv/p/sub/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "resolves to /srv/real/sub") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -L /srv/real/l && test -L /srv/real/sub/l && mkdir -m 0777 /srv/open && chmod 0777 /srv/open && ln -s /target /srv/open/l")
		if r := b.remove(t, "/srv/open/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "without the sticky bit") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -L /srv/open/l && chmod 1777 /srv/open")
		if r := b.remove(t, "/srv/open/l", Expect{Kind: Symlink, Value: "/target"}, ""); r.Outcome != Removed {
			t.Fatalf("sticky: %+v", r)
		}
	})
	t.Run("nothing there, or no parent, is absent", func(t *testing.T) {
		if r := b.remove(t, "/srv/a/none", Expect{Kind: EmptyDir}, ""); r.Outcome != Absent {
			t.Fatalf("%+v", r)
		}
		if r := b.remove(t, "/srv/none/x", Expect{Kind: EmptyDir}, ""); r.Outcome != Absent {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a machine without perl is refused", func(t *testing.T) {
		b.sh(t, "mv /usr/bin/perl /usr/bin/perl.away")
		defer b.sh(t, "mv /usr/bin/perl.away /usr/bin/perl")
		command, _ := Command("/srv/a/x", Expect{Kind: EmptyDir})
		code, out, errOut := b.run(t, command, "")
		if _, err := Parse(code, out, errOut); code != 127 || err == nil || !strings.Contains(err.Error(), "perl is not installed") {
			t.Fatalf("exit %d: %v", code, err)
		}
	})
}

func TestInstallRealShell(t *testing.T) {
	b := start(t)
	b.sh(t, "mkdir -p /srv/i && useradd -M svc")
	noStaging := func(t *testing.T) {
		t.Helper()
		if got := b.sh(t, "ls -A /srv/i | grep homelab-safeops || true"); got != "" {
			t.Fatalf("staging left: %q", got)
		}
	}
	t.Run("create installs a new file with its owner and mode, bytes exact", func(t *testing.T) {
		content := "line one\n\x00\xffbinary\n"
		if r := b.install(t, "/srv/i/f", Create, "4750", "svc", "root", content, ""); r.Outcome != Installed || r.Path != "" {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "stat -c '%a %U %G' /srv/i/f"); got != "4750 svc root\n" {
			t.Fatalf("%q", got)
		}
		if got := b.sh(t, "od -An -c /srv/i/f | tr -s ' '"); !strings.Contains(got, "\\0 377") {
			t.Fatalf("content %q", got)
		}
		noStaging(t)
	})
	t.Run("create moves aside what is there, and keeps it unless it is the same content", func(t *testing.T) {
		b.sh(t, "echo theirs > /srv/i/g")
		r := b.install(t, "/srv/i/g", Create, "0644", "root", "root", "ours\n", "")
		if r.Outcome != Installed || !strings.HasPrefix(r.Path, "/srv/i/.homelab-safeops.") || !strings.HasSuffix(r.Path, ".kept") {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/i/g "+r.Path); got != "ours\ntheirs\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm "+r.Path+" && echo ours > /srv/i/h")
		if r := b.install(t, "/srv/i/h", Create, "0644", "root", "root", "ours\n", ""); r.Outcome != Installed || r.Path != "" {
			t.Fatalf("same content: %+v", r)
		}
		noStaging(t)
	})
	t.Run("create never replaces something that appears while it writes", func(t *testing.T) {
		b.sh(t, "echo theirs > /srv/i/k")
		r := b.install(t, "/srv/i/k", Create, "0644", "root", "root", "ours\n", `[ "$1" = aside ] && echo racer > k`)
		if r.Outcome != Kept || !strings.HasSuffix(r.Path, ".kept") || !strings.Contains(r.Reason, "File exists") {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/i/k "+r.Path); got != "racer\ntheirs\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm "+r.Path)
		noStaging(t)
	})
	t.Run("update replaces the declared file, a directory is refused", func(t *testing.T) {
		if r := b.install(t, "/srv/i/f", Update, "0600", "root", "root", "new\n", ""); r.Outcome != Installed || r.Path != "" {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/i/f; stat -c %a /srv/i/f"); got != "new\n600\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "mkdir /srv/i/d")
		for _, how := range []InstallMode{Create, Update} {
			if r := b.install(t, "/srv/i/d", how, "0644", "root", "root", "x", ""); r.Outcome != Refused || !strings.Contains(r.Reason, "is a directory") {
				t.Fatalf("%s: %+v", how, r)
			}
		}
		noStaging(t)
	})
	t.Run("a bad mode, owner or group is refused, nothing written", func(t *testing.T) {
		for _, c := range [][3]string{{"rw", "root", "root"}, {"0644", "nobody-such", "root"}, {"0644", "root", "nobody-such"}} {
			if r := b.install(t, "/srv/i/n", Create, c[0], c[1], c[2], "x", ""); r.Outcome != Refused {
				t.Fatalf("%v: %+v", c, r)
			}
		}
		b.sh(t, "test ! -e /srv/i/n")
		noStaging(t)
	})
	t.Run("paths under /lib resolve to /usr/lib, which is what is pinned", func(t *testing.T) {
		command, _ := ResolveCommand("/lib/homelab-test.conf")
		resolved, err := ParseResolved(b.run(t, command, ""))
		if err != nil || resolved != "/usr/lib/homelab-test.conf" {
			t.Fatalf("%q %v", resolved, err)
		}
		if r := b.install(t, "/lib/homelab-test.conf", Create, "0644", "root", "root", "x\n", ""); r.Outcome != Refused || !strings.Contains(r.Reason, "the parent /lib is a symlink") {
			t.Fatalf("unresolved: %+v", r)
		}
		if r := b.install(t, resolved, Create, "0644", "root", "root", "x\n", ""); r.Outcome != Installed {
			t.Fatalf("resolved: %+v", r)
		}
		d := b.digest(t, resolved, File)
		if r := b.remove(t, resolved, Expect{Kind: File, Value: d}, ""); r.Outcome != Removed {
			t.Fatalf("remove: %+v", r)
		}
		b.sh(t, "test ! -e /usr/lib/homelab-test.conf")
	})
	t.Run("without sha256sum a digest, a file removal and a create are refused, nothing touched", func(t *testing.T) {
		b.sh(t, "echo x > /srv/i/s && mv /usr/bin/sha256sum /usr/bin/sha256sum.away")
		defer b.sh(t, "mv /usr/bin/sha256sum.away /usr/bin/sha256sum")
		command, _ := DigestCommand("/srv/i/s", File)
		if _, err := ParseDigest(b.run(t, command, "")); err == nil || !strings.Contains(err.Error(), "sha256sum (coreutils) is not installed") {
			t.Fatalf("digest: %v", err)
		}
		if r := b.remove(t, "/srv/i/s", Expect{Kind: File, Value: strings.Repeat("0", 64)}, ""); r.Outcome != Refused || !strings.Contains(r.Reason, "sha256sum") {
			t.Fatalf("remove: %+v", r)
		}
		if r := b.install(t, "/srv/i/s", Create, "0644", "root", "root", "y\n", ""); r.Outcome != Refused || !strings.Contains(r.Reason, "sha256sum") {
			t.Fatalf("install: %+v", r)
		}
		b.sh(t, `test "$(cat /srv/i/s)" = x`)
		noStaging(t)
	})
}

// Where renameat2 answers ENOSYS (an old kernel, or here a seccomp filter), nothing is captured,
// so nothing is touched: there is no fallback to rename(2), which could replace.
func TestWithoutRenameat2(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "seccomp.json")
	if err := os.WriteFile(profile, []byte(`{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[{"names":["renameat2"],"action":"SCMP_ACT_ERRNO","errnoRet":38}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := start(t, "--security-opt", "seccomp="+profile)
	b.sh(t, "mkdir -p /srv/a/tree && echo keep > /srv/a/tree/f && ln -s /target /srv/a/l")
	for _, c := range []struct {
		p    string
		want Expect
	}{{"/srv/a/tree", Expect{Kind: EmptyDir}}, {"/srv/a/l", Expect{Kind: Symlink, Value: "/target"}}} {
		r := b.remove(t, c.p, c.want, "")
		if r.Outcome != Refused || !strings.Contains(r.Reason, "Function not implemented") {
			t.Fatalf("%s: %+v", c.p, r)
		}
	}
	b.sh(t, `test "$(cat /srv/a/tree/f)" = keep && test -L /srv/a/l && test -z "$(ls -A /srv/a | grep homelab-safeops)"`)
}
