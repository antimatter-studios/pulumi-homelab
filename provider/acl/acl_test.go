package acl

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

var pool = strings.Join([]string{
	"user::rwx",
	"user:svc:rwx",
	"group::rwx",
	"group:rwgroup:rwx",
	"group:rogroup:r-x",
	"mask::rwx",
	"other::r-x",
	"default:user::rwx",
	"default:user:svc:rwx",
	"default:group::rwx",
	"default:group:rwgroup:rwx",
	"default:mask::rwx",
	"default:other::r-x",
}, "\n")

func keys(entries []Entry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, EntryKey(e.Type, e.Name))
	}
	return out
}

// must fails the test (by panicking) on an error the TypeScript would have thrown.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestNormalisePerms(t *testing.T) {
	check := func(t *testing.T, in, want string) {
		t.Helper()
		if got := must(NormalisePerms(in)); got != want {
			t.Errorf("NormalisePerms(%q) = %q, want %q", in, got, want)
		}
	}
	refuse := func(t *testing.T, in, contains string) {
		t.Helper()
		_, err := NormalisePerms(in)
		if err == nil || !strings.Contains(err.Error(), contains) {
			t.Errorf("NormalisePerms(%q) error = %v, want one containing %q", in, err, contains)
		}
	}
	t.Run("settles every spelling on the three characters getfacl prints", func(t *testing.T) {
		check(t, "rwx", "rwx")
		check(t, "rw", "rw-")
		check(t, "r-x", "r-x")
		check(t, "x", "--x")
		check(t, "-", "---")
	})
	t.Run("takes an octal digit, which is how a mode is usually thought about", func(t *testing.T) {
		check(t, "7", "rwx")
		check(t, "5", "r-x")
		check(t, "6", "rw-")
		check(t, "0", "---")
	})
	t.Run("refuses the conditional X, which can only ever read as drift", func(t *testing.T) {
		refuse(t, "rwX", "drift")
	})
	t.Run("refuses anything that is not a permission set", func(t *testing.T) {
		refuse(t, "rwxr", "")
		refuse(t, "8", "")
		refuse(t, "", "")
	})
}

func TestParseGetfacl(t *testing.T) {
	t.Run("reads both scopes apart", func(t *testing.T) {
		access, defaults := ParseGetfacl(pool)
		if !slices.Contains(keys(access), "user:svc") || !slices.Contains(keys(defaults), "user:svc") {
			t.Errorf("access %v, defaults %v", keys(access), keys(defaults))
		}
		if slices.Contains(keys(access), "group:rogroup:default") {
			t.Errorf("access %v", keys(access))
		}
		if slices.Contains(keys(defaults), "group:rogroup") {
			t.Errorf("defaults %v", keys(defaults))
		}
	})
	t.Run("keeps the owning entries distinguishable from the named ones", func(t *testing.T) {
		access, _ := ParseGetfacl(pool)
		find := func(name string) string {
			for _, e := range access {
				if e.Type == User && e.Name == name {
					return e.Perms
				}
			}
			return "<none>"
		}
		if find("") != "rwx" || find("svc") != "rwx" {
			t.Errorf("owner %q, svc %q", find(""), find("svc"))
		}
	})
	t.Run("reports what a mask actually leaves, not what the entry claims", func(t *testing.T) {
		access, _ := ParseGetfacl("user:svc:rwx\t#effective:r--\nmask::r--\n")
		if access[0].Perms != "rwx" || access[0].Effective != "r--" {
			t.Errorf("got %+v", access[0])
		}
	})
	t.Run("treats an entry with no effective comment as granting what it says", func(t *testing.T) {
		access, _ := ParseGetfacl("user:svc:rwx\n")
		if access[0].Effective != "rwx" {
			t.Errorf("got %+v", access[0])
		}
	})
	t.Run("skips the comment header without skipping the entries", func(t *testing.T) {
		withHeader, _ := ParseGetfacl("# file: mnt/pool\n# owner: root\n# group: rwgroup\n# flags: -s-\n" + pool)
		plain, _ := ParseGetfacl(pool)
		if len(withHeader) != len(plain) {
			t.Errorf("%d entries with header, %d without", len(withHeader), len(plain))
		}
	})
	t.Run("survives the blank lines getfacl puts between paths", func(t *testing.T) {
		access, _ := ParseGetfacl("user:svc:rwx\n\n\nuser:other:r-x\n")
		if len(access) != 2 {
			t.Errorf("got %d entries", len(access))
		}
	})
}

func TestFindACLEntry(t *testing.T) {
	actual, _ := ParseGetfacl(pool)
	nobody := EntryArgs{Type: User, Name: "nobody", Perms: "rwx"}
	t.Run("comes back absent rather than missing", func(t *testing.T) {
		found := FindACLEntry(actual, nobody)
		if found.Perms != "" || found.Effective != "" {
			t.Errorf("got %+v", found)
		}
	})
	t.Run("is not the same as an entry granting nothing", func(t *testing.T) {
		if FindACLEntry(actual, nobody).Perms == must(NormalisePerms("---")) {
			t.Error("absent read as ---")
		}
	})
	t.Run("is never satisfied", func(t *testing.T) {
		if must(EntrySatisfied(FindACLEntry(actual, nobody), nobody)) {
			t.Error("absent entry satisfied")
		}
	})
}

func TestEntrySatisfied(t *testing.T) {
	wanted := EntryArgs{Type: User, Name: "svc", Perms: "rwx"}
	with := func(perms string) EntryArgs { w := wanted; w.Perms = perms; return w }
	check := func(t *testing.T, actual Entry, wanted EntryArgs, want bool) {
		t.Helper()
		if got := must(EntrySatisfied(actual, wanted)); got != want {
			t.Errorf("EntrySatisfied(%+v, %+v) = %v, want %v", actual, wanted, got, want)
		}
	}
	t.Run("is satisfied when both what it says and what it grants match", func(t *testing.T) {
		check(t, Entry{User, "svc", "rwx", "rwx"}, wanted, true)
	})
	t.Run("is not satisfied when a mask has suppressed it", func(t *testing.T) {
		check(t, Entry{User, "svc", "rwx", "r-x"}, wanted, false)
	})
	t.Run("is not satisfied when the entry says more than was asked for", func(t *testing.T) {
		check(t, Entry{User, "svc", "rwx", "r-x"}, with("r-x"), false)
	})
	t.Run("compares the settled spelling, not the one that was typed", func(t *testing.T) {
		check(t, Entry{User, "svc", "rw-", "rw-"}, with("rw"), true)
		check(t, Entry{User, "svc", "rwx", "rwx"}, with("7"), true)
	})
}

func TestUnsatisfiedAbandoned(t *testing.T) {
	access, _ := ParseGetfacl(pool)
	t.Run("leaves alone what the machine already agrees about", func(t *testing.T) {
		if todo := must(Unsatisfied(access, []EntryArgs{{User, "svc", "rwx"}}, Access)); len(todo) != 0 {
			t.Errorf("got %+v", todo)
		}
	})
	t.Run("picks out the entry that is missing", func(t *testing.T) {
		todo := must(Unsatisfied(access, []EntryArgs{{User, "svc", "rwx"}, {User, "backup", "r-x"}}, Access))
		var names []string
		for _, c := range todo {
			names = append(names, c.Entry.Name)
		}
		if !reflect.DeepEqual(names, []string{"backup"}) {
			t.Errorf("got %v", names)
		}
	})
	t.Run("picks out the entry whose permissions moved", func(t *testing.T) {
		if todo := must(Unsatisfied(access, []EntryArgs{{User, "svc", "r-x"}}, Access)); len(todo) != 1 {
			t.Errorf("got %+v", todo)
		}
	})
	t.Run("takes away only what this resource used to declare", func(t *testing.T) {
		previous := []EntryArgs{{User, "svc", "rwx"}, {User, "old", "r-x"}}
		var names []string
		for _, e := range Abandoned(previous, []EntryArgs{{User, "svc", "rwx"}}) {
			names = append(names, e.Name)
		}
		if !reflect.DeepEqual(names, []string{"old"}) {
			t.Errorf("got %v", names)
		}
	})
	t.Run("does not abandon an entry whose permissions merely changed", func(t *testing.T) {
		if gone := Abandoned([]EntryArgs{{User, "svc", "rwx"}}, []EntryArgs{{User, "svc", "r-x"}}); len(gone) != 0 {
			t.Errorf("got %+v", gone)
		}
	})
}

func TestFormatEntry(t *testing.T) {
	t.Run("writes the owning entries with an empty name, as getfacl does", func(t *testing.T) {
		if got := must(FormatEntry(EntryArgs{Type: User, Perms: "rwx"}, Default)); got != "default:user::rwx" {
			t.Errorf("FormatEntry = %q", got)
		}
		if got := FormatRemoval(Other, "", Default); got != "default:other:" {
			t.Errorf("FormatRemoval = %q", got)
		}
	})
	t.Run("settles the permissions on the way out", func(t *testing.T) {
		if got := must(FormatEntry(EntryArgs{Type: Group, Name: "ro", Perms: "5"}, Access)); got != "group:ro:r-x" {
			t.Errorf("FormatEntry = %q", got)
		}
	})
}

// Whitespace is JavaScript's, as String.prototype.trim sees it: a BOM is trimmed and NEL is not.
func TestJavaScriptWhitespace(t *testing.T) {
	if got, err := NormalisePerms("\ufeffrw"); err != nil || got != "rw-" {
		t.Errorf("BOM: %q %v", got, err)
	}
	if _, err := NormalisePerms("\u0085rw"); err == nil {
		t.Error("NEL was trimmed")
	}
	access, _ := ParseGetfacl("\ufeffuser:svc:rwx\t#effective:r-x\n")
	if len(access) != 1 || access[0].Effective != "r-x" {
		t.Errorf("BOM line: %+v", access)
	}
	access, _ = ParseGetfacl("user:svc:rwx\t#effective:\u0085r-x\u0085\n")
	if len(access) != 1 || access[0].Effective != "\u0085r-x\u0085" {
		t.Errorf("NEL effective: %+v", access)
	}
}
