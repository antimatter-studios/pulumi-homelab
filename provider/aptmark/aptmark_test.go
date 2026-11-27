package aptmark

import (
	"reflect"
	"strings"
	"testing"
)

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseManual(t *testing.T) {
	t.Run("takes one name per line", func(t *testing.T) {
		eq(t, ParseManual("ovmf\nqemu-efi-arm\nqemu-system-arm\n"), []string{"ovmf", "qemu-efi-arm", "qemu-system-arm"})
	})
	t.Run("answers empty for a machine that holds none of them", func(t *testing.T) {
		eq(t, ParseManual(""), []string{})
		eq(t, ParseManual("\n\n"), []string{})
	})
	t.Run("strips the architecture, since a declaration carries none", func(t *testing.T) {
		eq(t, ParseManual("libc6:arm64\n"), []string{"libc6"})
	})
	t.Run("ignores commentary apt may print", func(t *testing.T) {
		eq(t, ParseManual("# comment\novmf\n"), []string{"ovmf"})
	})
}

func TestAutoMarked(t *testing.T) {
	installed := map[string]string{"ovmf": "2024.02", "qemu-efi-arm": "2024.02", "qemu-system-arm": "8.2"}
	t.Run("is content when everything declared is held as manual", func(t *testing.T) {
		eq(t, AutoMarked([]string{"ovmf"}, installed, []string{"ovmf", "other"}), []string{})
	})
	t.Run("names a declared package apt holds as auto", func(t *testing.T) {
		eq(t, AutoMarked([]string{"ovmf", "qemu-efi-arm"}, installed, []string{"qemu-efi-arm"}), []string{"ovmf"})
	})
	t.Run("says nothing about a package that is not installed at all", func(t *testing.T) {
		eq(t, AutoMarked([]string{"not-there"}, installed, []string{}), []string{})
	})
	t.Run("says nothing about a package nothing declared", func(t *testing.T) {
		eq(t, AutoMarked([]string{}, installed, []string{}), []string{})
	})
	t.Run("matches a declared name against a multi-arch installed one", func(t *testing.T) {
		eq(t, AutoMarked([]string{"libc6"}, map[string]string{"libc6:arm64": "2.36"}, []string{}), []string{"libc6"})
		eq(t, AutoMarked([]string{"libc6"}, map[string]string{"libc6:arm64": "2.36"}, []string{"libc6"}), []string{})
	})
	t.Run("keeps the declared order, so a message reads the way the code does", func(t *testing.T) {
		eq(t, AutoMarked([]string{"qemu-system-arm", "ovmf"}, installed, []string{}), []string{"qemu-system-arm", "ovmf"})
	})
}

func TestMarkManualCommand(t *testing.T) {
	must := func(t *testing.T, names []string) string {
		t.Helper()
		command, ok := MarkManualCommand(names)
		if !ok {
			t.Fatalf("MarkManualCommand(%q) gave no command", names)
		}
		return command
	}
	contains := func(t *testing.T, s, sub string) {
		t.Helper()
		if !strings.Contains(s, sub) {
			t.Errorf("%q does not contain %q", s, sub)
		}
	}
	t.Run("writes nothing when there is nothing to mark", func(t *testing.T) {
		if command, ok := MarkManualCommand([]string{}); ok {
			t.Errorf("got %q, want none", command)
		}
	})
	t.Run("marks the whole set in one command", func(t *testing.T) {
		command := must(t, []string{"ovmf", "qemu-efi-arm"})
		if n := strings.Count(command, "apt-mark"); n != 1 {
			t.Errorf("apt-mark appears %d times", n)
		}
		contains(t, command, "'ovmf'")
		contains(t, command, "'qemu-efi-arm'")
	})
	t.Run("never marks anything auto", func(t *testing.T) {
		if command := must(t, []string{"ovmf"}); strings.Contains(command, "auto") {
			t.Errorf("%q mentions auto", command)
		}
	})
	t.Run("quotes the names, which come out of a declaration", func(t *testing.T) {
		contains(t, must(t, []string{"it's"}), `'it'\''s'`)
	})
	t.Run("is the same string for the same set, however it was ordered", func(t *testing.T) {
		if a, b := must(t, []string{"b", "a"}), must(t, []string{"a", "b"}); a != b {
			t.Errorf("%q != %q", a, b)
		}
	})
	t.Run("says nothing on stdout, since the answer comes from the read that follows", func(t *testing.T) {
		contains(t, must(t, []string{"ovmf"}), ">/dev/null")
	})
}
