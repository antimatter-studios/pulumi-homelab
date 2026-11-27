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

// Real-shell tests in a Debian 13 container, as root, with a tmpfs mounted at /srv/mnt so a mount
// point can stand for an entry on another filesystem.
type box struct{ id string }

func start(t *testing.T, opts ...string) box {
	t.Helper()
	args := append([]string{"run", "-d", "--rm", "--tmpfs", "/srv/mnt"}, opts...)
	args = append(args, "debian:trixie-slim", "sleep", "infinity")
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
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

func (b box) remove(t *testing.T, p string, want Expect) Result {
	t.Helper()
	command, err := Command(p, want)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("docker", "exec", b.id, "sh", "-c", command)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
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

func TestRealShell(t *testing.T) {
	b := start(t)
	b.sh(t, "mkdir -p /srv/a /srv/real && ln -s /srv/real /srv/p")
	noStaging := func(t *testing.T, dir string) {
		t.Helper()
		if got := b.sh(t, "ls -A "+dir+" | grep homelab-safeops || true"); got != "" {
			t.Fatalf("staging left in %s: %q", dir, got)
		}
	}

	t.Run("a matching symlink is removed", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/a/l")
		if r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Target: "/target"}); r.Outcome != Removed || r.Path != "" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/l && test ! -L /srv/a/l")
		noStaging(t, "/srv/a")
	})
	t.Run("a mismatching symlink is restored untouched", func(t *testing.T) {
		b.sh(t, "ln -s /elsewhere /srv/a/l")
		r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Target: "/target"})
		if r.Outcome != Restored || r.Reason != "it points somewhere else" {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "readlink /srv/a/l"); got != "/elsewhere\n" {
			t.Fatalf("%q", got)
		}
		noStaging(t, "/srv/a")
		b.sh(t, "rm /srv/a/l")
	})
	t.Run("a regular file where a symlink was expected is restored", func(t *testing.T) {
		b.sh(t, "echo data > /srv/a/l")
		if r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Target: "/target"}); r.Outcome != Restored || r.Reason != "it is not a symlink" {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/a/l"); got != "data\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm /srv/a/l")
	})
	t.Run("a directory that is not the resource's is restored with its contents", func(t *testing.T) {
		b.sh(t, "mkdir /srv/a/d && echo keep > /srv/a/d/f")
		if r := b.remove(t, "/srv/a/d", Expect{Kind: File}); r.Outcome != Restored {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test \"$(cat /srv/a/d/f)\" = keep")
		noStaging(t, "/srv/a")
	})
	t.Run("a directory that is the resource's is removed with its contents", func(t *testing.T) {
		if r := b.remove(t, "/srv/a/d", Expect{Kind: Dir, DigestCommand: `cat "$1/f"`, Digest: "keep"}); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/d")
	})
	t.Run("an empty directory is removed, and one with anything in it is restored", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/e /srv/a/full && touch /srv/a/full/data")
		if r := b.remove(t, "/srv/a/full", Expect{Kind: EmptyDir}); r.Outcome != Restored || r.Reason != "it is not empty" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -f /srv/a/full/data")
		if r := b.remove(t, "/srv/a/e", Expect{Kind: EmptyDir}); r.Outcome != Removed || r.Path != "" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/e")
		noStaging(t, "/srv/a")
	})
	t.Run("a directory is restored onto its absent name with renameat2", func(t *testing.T) {
		b.sh(t, "mkdir -p /srv/a/tree/sub && echo keep > /srv/a/tree/sub/f")
		if r := b.remove(t, "/srv/a/tree", Expect{Kind: Dir, DigestCommand: "echo other", Digest: "mine"}); r.Outcome != Restored || r.Path != "" {
			t.Fatalf("%+v", r)
		}
		b.sh(t, `test "$(cat /srv/a/tree/sub/f)" = keep`)
		noStaging(t, "/srv/a")
	})
	t.Run("a directory restore never replaces an empty directory that appeared at the name", func(t *testing.T) {
		// rename(2) would silently replace an empty directory here; renameat2 with
		// RENAME_NOREPLACE refuses, so the tree stays in the private directory, reported
		r := b.remove(t, "/srv/a/tree", Expect{Kind: Dir, DigestCommand: "mkdir /srv/a/tree; echo other", Digest: "mine"})
		if r.Outcome != Kept || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") || !strings.HasSuffix(r.Path, "/entry") || !strings.Contains(r.Reason, "File exists") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -d /srv/a/tree && test -z \"$(ls -A /srv/a/tree)\" && test \"$(cat "+r.Path+"/sub/f)\" = keep")
		b.sh(t, "rm -r /srv/a/tree /srv/a/.homelab-safeops.*")
	})
	t.Run("a file is removed only when its digest matches", func(t *testing.T) {
		b.sh(t, "echo content > /srv/a/f")
		sum := strings.Fields(b.sh(t, "sha256sum /srv/a/f"))[0]
		digest := `sha256sum < "$1" | cut -d" " -f1`
		if r := b.remove(t, "/srv/a/f", Expect{Kind: File, DigestCommand: digest, Digest: "0" + sum[1:]}); r.Outcome != Restored || r.Reason != "its content is not what was written" {
			t.Fatalf("%+v", r)
		}
		if r := b.remove(t, "/srv/a/f", Expect{Kind: File, DigestCommand: digest, Digest: sum}); r.Outcome != Removed {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test ! -e /srv/a/f")
	})
	t.Run("a restore that would clobber something new keeps the entry and reports its exact path", func(t *testing.T) {
		b.sh(t, "ln -s /elsewhere /srv/a/l")
		// the digest command stands for whatever writes a new file at the name meanwhile
		r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Target: "/elsewhere", DigestCommand: `echo new > /srv/a/l; echo x`, Digest: "y"})
		if r.Outcome != Kept || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") || !strings.HasSuffix(r.Path, "/entry") || !strings.Contains(r.Reason, "File exists") {
			t.Fatalf("%+v", r)
		}
		if got := b.sh(t, "cat /srv/a/l; readlink "+r.Path); got != "new\n/elsewhere\n" {
			t.Fatalf("%q", got)
		}
		b.sh(t, "rm -r /srv/a/l /srv/a/.homelab-safeops.*")
	})
	t.Run("a private directory that cannot be cleaned up is reported", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/a/l")
		r := b.remove(t, "/srv/a/l", Expect{Kind: Symlink, Target: "/target", DigestCommand: `touch "$(dirname "$1")/stray"; echo d`, Digest: "d"})
		if r.Outcome != Removed || !strings.HasPrefix(r.Path, "/srv/a/.homelab-safeops.") || !strings.Contains(r.Reason, "Directory not empty") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -f "+r.Path+"/stray && rm -r "+r.Path)
	})
	t.Run("an entry on another filesystem is refused, never copied", func(t *testing.T) {
		b.sh(t, "echo inside > /srv/mnt/f")
		r := b.remove(t, "/srv/mnt", Expect{Kind: Dir})
		if r.Outcome != Refused || !strings.Contains(r.Reason, "could not be captured") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test \"$(cat /srv/mnt/f)\" = inside && mountpoint -q /srv/mnt || grep -q ' /srv/mnt ' /proc/mounts")
		noStaging(t, "/srv")
	})
	t.Run("a symlink target with a trailing newline is compared exactly", func(t *testing.T) {
		b.sh(t, `perl -e 'symlink("/target\n", "/srv/a/n") or die'`)
		if r := b.remove(t, "/srv/a/n", Expect{Kind: Symlink, Target: "/target"}); r.Outcome != Restored {
			t.Fatalf("without the newline: %+v", r)
		}
		if r := b.remove(t, "/srv/a/n", Expect{Kind: Symlink, Target: "/target\n"}); r.Outcome != Removed {
			t.Fatalf("with the newline: %+v", r)
		}
	})
	t.Run("a parent swapped for a symlink is refused", func(t *testing.T) {
		b.sh(t, "ln -s /target /srv/real/l")
		r := b.remove(t, "/srv/p/l", Expect{Kind: Symlink, Target: "/target"})
		if r.Outcome != Refused || !strings.Contains(r.Reason, "the parent /srv/p is a symlink") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -L /srv/real/l")
		// and a parent under a symlinked ancestor resolves elsewhere
		b.sh(t, "mkdir /srv/real/sub && ln -s /target /srv/real/sub/l")
		r = b.remove(t, "/srv/p/sub/l", Expect{Kind: Symlink, Target: "/target"})
		if r.Outcome != Refused || !strings.Contains(r.Reason, "resolves to /srv/real/sub") {
			t.Fatalf("%+v", r)
		}
		b.sh(t, "test -L /srv/real/sub/l")
	})
	t.Run("nothing there, or no parent, is absent", func(t *testing.T) {
		if r := b.remove(t, "/srv/a/none", Expect{Kind: File}); r.Outcome != Absent {
			t.Fatalf("%+v", r)
		}
		if r := b.remove(t, "/srv/none/x", Expect{Kind: File}); r.Outcome != Absent {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("a machine without perl is refused", func(t *testing.T) {
		b.sh(t, "mv /usr/bin/perl /usr/bin/perl.away")
		defer b.sh(t, "mv /usr/bin/perl.away /usr/bin/perl")
		command, _ := Command("/srv/a/x", Expect{Kind: File})
		out, _ := exec.Command("docker", "exec", b.id, "sh", "-c", command).CombinedOutput()
		if _, err := Parse(127, "", string(out)); err == nil || !strings.Contains(err.Error(), "perl is not installed") {
			t.Fatalf("%v %s", err, out)
		}
		if err := exec.Command("docker", "exec", b.id, "sh", "-c", command).Run(); err == nil {
			t.Fatal("ran without perl")
		} else if exit := new(exec.ExitError); errors.As(err, &exit) && exit.ExitCode() != 127 {
			t.Fatalf("exit %d", exit.ExitCode())
		}
	})
}

// Where renameat2 is unavailable (ENOSYS, as an old kernel or a seccomp filter answers), a
// directory that is not the resource's is kept in the private directory and reported; it is never
// put back with rename(2), which could replace an empty directory at the name.
func TestDirectoryRestoreWithoutRenameat2(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "seccomp.json")
	if err := os.WriteFile(profile, []byte(`{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[{"names":["renameat2"],"action":"SCMP_ACT_ERRNO","errnoRet":38}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := start(t, "--security-opt", "seccomp="+profile)
	b.sh(t, "mkdir -p /srv/a/tree && echo keep > /srv/a/tree/f")
	r := b.remove(t, "/srv/a/tree", Expect{Kind: Dir, DigestCommand: "echo other", Digest: "mine"})
	if r.Outcome != Kept || !strings.HasSuffix(r.Path, "/entry") || !strings.Contains(r.Reason, "Function not implemented") {
		t.Fatalf("%+v", r)
	}
	b.sh(t, "test ! -e /srv/a/tree && test \"$(cat "+r.Path+"/f)\" = keep")
}
