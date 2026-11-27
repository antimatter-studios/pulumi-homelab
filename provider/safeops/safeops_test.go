package safeops

import (
	"strings"
	"testing"
)

func TestSplit(t *testing.T) {
	for p, want := range map[string][2]string{"/var/log/journal": {"/var/log", "journal"}, "/journal": {"/", "journal"}} {
		parent, name, err := Split(p)
		if err != nil || parent != want[0] || name != want[1] {
			t.Errorf("Split(%q) = %q %q %v", p, parent, name, err)
		}
	}
	for _, bad := range []string{"", "/", "journal", "/a/", "/a/../b", "/a/./b", "//a"} {
		if _, _, err := Split(bad); err == nil {
			t.Errorf("Split(%q) accepted", bad)
		}
	}
}

func TestCommand(t *testing.T) {
	got, err := Command("/it's/x", Expect{Kind: Symlink, Value: "/t'"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "perl -e '") || !strings.HasSuffix(got, ` -- 'remove' '/it'\''s' 'x' 'symlink' '/t'\'''`) {
		t.Fatalf("got %s", got[len(got)-80:])
	}
	if _, err := Command("/a/b", Expect{Kind: "dir"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if _, err := Command("relative", Expect{Kind: File}); err == nil {
		t.Fatal("relative path accepted")
	}
	d, err := DigestCommand("/a/b", Tree)
	if err != nil || !strings.HasSuffix(d, ` -- 'digest' '/a' 'b' 'tree'`) {
		t.Fatalf("digest command %v", err)
	}
	if _, err := DigestCommand("/a/b", Symlink); err == nil {
		t.Fatal("a symlink digest accepted")
	}
	// the utilities this replaces appear nowhere, and the caller supplies no command
	for _, banned := range []string{"'mv'", " mv ", "rm -rf", `"rm"`} {
		if strings.Contains(script, banned) {
			t.Fatalf("the script uses %s", banned)
		}
	}
}

func TestParse(t *testing.T) {
	ok := "kept\x00/a/.homelab-safeops.ab/entry\x00it points somewhere else, and it could not be put back: File exists\x00/a\x00.homelab-safeops.ab\x0061:12\x000\x00"
	r, err := Parse(0, ok, "")
	if err != nil || r.Outcome != Kept || r.Path != "/a/.homelab-safeops.ab/entry" || r.Staging != ".homelab-safeops.ab" || r.ParentDevIno != "61:12" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = Parse(0, "partial\x00/a/.p\x00removal stopped: x\x00/a\x00.p\x001:2\x003\x00", "")
	if err != nil || r.Outcome != Partial || r.Remaining != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	for _, bad := range []string{
		"", "nonsense", strings.TrimSuffix(ok, "\x00"), ok + "extra\x00", ok + "x",
		"maybe\x00\x00\x00/a\x00\x00\x000\x00", "removed\x00\x00\x00/a\x00\x00\x00-1\x00", "removed\x00\x00\x00/a\x00\x00\x00x\x00",
	} {
		if _, err := Parse(0, bad, ""); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if _, err := Parse(127, "", "sh: perl: not found"); err == nil || !strings.Contains(err.Error(), "perl is not installed") {
		t.Fatalf("no perl: %v", err)
	}
	if _, err := Parse(255, "", "Died"); err == nil {
		t.Fatal("a fault was a result")
	}
	d, err := ParseDigest(0, "digest\x00\x00"+strings.Repeat("ab", 32)+"\x00/a\x00\x001:2\x000\x00", "")
	if err != nil || d != strings.Repeat("ab", 32) {
		t.Fatalf("%q %v", d, err)
	}
	if _, err := ParseDigest(0, "refused\x00\x00it is not a regular file\x00/a\x00\x001:2\x000\x00", ""); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("refused digest: %v", err)
	}
	if _, err := ParseDigest(0, "digest\x00\x00\x00/a\x00\x001:2\x000\x00", ""); err == nil {
		t.Fatal("an empty digest accepted")
	}
}

func TestWarning(t *testing.T) {
	for _, c := range []struct {
		r    Result
		want string
	}{
		{Result{Outcome: Absent}, ""},
		{Result{Outcome: Removed}, ""},
		{Result{Outcome: Removed, Path: "/a/.p", Reason: "x"}, "Symlink /a/b was removed, but /a/.p is left behind: x"},
		{Result{Outcome: Restored, Reason: "it points somewhere else"}, "Symlink /a/b was kept on the machine rather than deleted: it points somewhere else"},
		{Result{Outcome: Kept, Path: "/a/.p/entry", Reason: "r"}, "Symlink /a/b was kept on the machine rather than deleted, and is now in /a/.p/entry: r"},
		{Result{Outcome: Kept, Parent: "/a", Staging: ".p", ParentDevIno: "1:2", Reason: "r"}, "Symlink /a/b was kept on the machine rather than deleted, and is now in staging directory .p under the directory with dev:ino 1:2 (once /a): r"},
		{Result{Outcome: Partial, Path: "/a/.p", Remaining: 2, Reason: "e"}, "Symlink /a/b was only partly removed: 2 entries are left in /a/.p, first error: e"},
		{Result{Outcome: Refused, Reason: "the parent /a is a symlink, not the recorded directory"}, "Symlink /a/b was kept on the machine rather than deleted: the parent /a is a symlink, not the recorded directory"},
	} {
		if got := c.r.Warning("Symlink /a/b"); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c.r, got, c.want)
		}
	}
}
