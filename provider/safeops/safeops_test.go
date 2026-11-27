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
	got, err := Command("/it's/x", Expect{Kind: Symlink, Target: "/t'"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "perl -e '") || !strings.HasSuffix(got, ` -- '/it'\''s' 'x' 'symlink' '/t'\''' '' ''`) {
		t.Fatalf("got %s", got[len(got)-80:])
	}
	if _, err := Command("/a/b", Expect{Kind: "tree"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
	if _, err := Command("relative", Expect{Kind: File}); err == nil {
		t.Fatal("relative path accepted")
	}
	// mv, the utility this replaces, appears nowhere
	if strings.Contains(script, " mv ") || strings.Contains(script, `"mv"`) {
		t.Fatal("the script uses mv")
	}
}

func TestParse(t *testing.T) {
	r, err := Parse(0, "kept\x00/a/.homelab-safeops.1.2/entry\x00it points somewhere else, and it could not be put back: File exists\x00", "")
	if err != nil || r.Outcome != Kept || r.Path != "/a/.homelab-safeops.1.2/entry" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := Parse(127, "", "sh: perl: not found"); err == nil || !strings.Contains(err.Error(), "perl is not installed") {
		t.Fatalf("no perl: %v", err)
	}
	if _, err := Parse(255, "", "Died"); err == nil {
		t.Fatal("a fault was a result")
	}
	if _, err := Parse(0, "nonsense", ""); err == nil {
		t.Fatal("nonsense was a result")
	}
	if _, err := Parse(0, "maybe\x00\x00\x00", ""); err == nil {
		t.Fatal("unknown outcome accepted")
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
		{Result{Outcome: Kept, Path: "/a/.p/entry", Reason: "r"}, "Symlink /a/b was kept on the machine rather than deleted, and is now at /a/.p/entry: r"},
		{Result{Outcome: Refused, Reason: "the parent /a is a symlink, not the recorded directory"}, "Symlink /a/b was kept on the machine rather than deleted: the parent /a is a symlink, not the recorded directory"},
	} {
		if got := c.r.Warning("Symlink /a/b"); got != c.want {
			t.Errorf("%+v: got %q", c.r, got)
		}
	}
}
