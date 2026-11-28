package homelab

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/acl"
)

func named(kind acl.EntryType, name, perms string) acl.EntryArgs {
	return acl.EntryArgs{Type: kind, Name: name, Perms: perms}
}

func declaredEntry(kind, name, perms string) AclEntryArgs {
	e := AclEntryArgs{Type: kind, Perms: perms}
	if name != "" {
		e.Name = &name
	}
	return e
}

func setfacl(t *testing.T, path string, set, remove []aclStep) string {
	t.Helper()
	got, err := setfaclCommand(path, set, remove)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Ported from src/resources/acl.test.ts › what may be declared.
func TestEntryRefusal(t *testing.T) {
	t.Run("allows a named user or group, which is the whole point of the resource", func(t *testing.T) {
		if r := entryRefusal(named(acl.User, "svc", "rwx"), acl.Access); r != "" {
			t.Fatal(r)
		}
		if r := entryRefusal(named(acl.Group, "rogroup", "r-x"), acl.Access); r != "" {
			t.Fatal(r)
		}
	})
	t.Run("allows the base entries as access entries, which on a file are its mode bits", func(t *testing.T) {
		for _, kind := range []acl.EntryType{acl.User, acl.Group, acl.Other} {
			if r := entryRefusal(named(kind, "", "rw-"), acl.Access); r != "" {
				t.Errorf("%s: %q", kind, r)
			}
		}
	})
	t.Run("allows the same base entries as defaults, because no mode sets those", func(t *testing.T) {
		if r := entryRefusal(named(acl.User, "", "rwx"), acl.Default); r != "" {
			t.Fatal(r)
		}
		if r := entryRefusal(named(acl.Other, "", "---"), acl.Default); r != "" {
			t.Fatal(r)
		}
	})
	t.Run("refuses a mask in either scope", func(t *testing.T) {
		for _, scope := range []acl.Scope{acl.Access, acl.Default} {
			if r := entryRefusal(named(acl.Mask, "", "rwx"), scope); !strings.Contains(r, "computed") {
				t.Errorf("%s: %q", scope, r)
			}
		}
	})
	t.Run("refuses an other entry that names somebody", func(t *testing.T) {
		if r := entryRefusal(named(acl.Other, "svc", "rwx"), acl.Access); !strings.Contains(r, "no name") {
			t.Fatal(r)
		}
	})
}

// Ported from src/resources/acl.test.ts › composing setfacl.
func TestSetfaclCommand(t *testing.T) {
	svc := named(acl.User, "svc", "rwx")
	old := named(acl.User, "old", "r-x")
	t.Run("has nothing to run when nothing needs changing", func(t *testing.T) {
		if got := setfacl(t, "/mnt/pool/p", nil, nil); got != "" {
			t.Fatal(got)
		}
	})
	t.Run("writes an access entry without the default prefix, and a default with it", func(t *testing.T) {
		if got := setfacl(t, "/p", []aclStep{{svc, acl.Access}}, nil); !strings.Contains(got, "user:svc:rwx") || strings.Contains(got, "default:") {
			t.Fatal(got)
		}
		if got := setfacl(t, "/p", []aclStep{{svc, acl.Default}}, nil); !strings.Contains(got, "default:user:svc:rwx") {
			t.Fatal(got)
		}
	})
	t.Run("removes before it sets", func(t *testing.T) {
		got := setfacl(t, "/p", []aclStep{{svc, acl.Default}}, []aclStep{{svc, acl.Access}})
		if strings.Index(got, "-x") >= strings.Index(got, "-m") {
			t.Fatal(got)
		}
	})
	t.Run("gives removals no permissions, which is the form setfacl wants", func(t *testing.T) {
		got := setfacl(t, "/p", nil, []aclStep{{old, acl.Access}})
		if !strings.Contains(got, "'user:old'") || strings.Contains(got, "r-x") {
			t.Fatal(got)
		}
	})
	t.Run("sets everything in one call rather than one call per entry", func(t *testing.T) {
		got := setfacl(t, "/p", []aclStep{{svc, acl.Access}, {svc, acl.Default}}, nil)
		if strings.Count(got, "setfacl") != 1 || !strings.Contains(got, "user:svc:rwx,default:user:svc:rwx") {
			t.Fatal(got)
		}
	})
	t.Run("stops when a step fails rather than running the next one", func(t *testing.T) {
		got := setfacl(t, "/p", []aclStep{{svc, acl.Access}}, []aclStep{{old, acl.Access}})
		if !strings.Contains(got, "&&") || strings.Contains(got, ";") {
			t.Fatal(got)
		}
	})
	t.Run("quotes the path and the entries", func(t *testing.T) {
		got := setfacl(t, "/mnt/it's", []aclStep{{svc, acl.Access}}, nil)
		if !strings.Contains(got, `'/mnt/it'\''s'`) || !strings.Contains(got, "'user:svc:rwx'") {
			t.Fatal(got)
		}
	})
	t.Run("never reaches for -b or --set, which would remove entries it does not own", func(t *testing.T) {
		b := regexp.MustCompile(`(^|\s)-{1,2}b\b`)
		for _, got := range []string{
			setfacl(t, "/p", []aclStep{{svc, acl.Access}}, nil),
			setfacl(t, "/p", nil, []aclStep{{old, acl.Access}}),
			setfacl(t, "/p", []aclStep{{svc, acl.Access}}, []aclStep{{old, acl.Access}}),
		} {
			if b.MatchString(got) || strings.Contains(got, "--set") || strings.Contains(got, "--remove-all") {
				t.Error(got)
			}
		}
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		if setfacl(t, "/p", []aclStep{{svc, acl.Access}}, nil) != setfacl(t, "/p", []aclStep{{svc, acl.Access}}, nil) {
			t.Fatal("differs")
		}
	})
	t.Run("is exactly the command the TypeScript composed", func(t *testing.T) {
		want := `setfacl -x 'user:old' '/p' && setfacl -m 'user:svc:rwx' '/p'`
		if got := setfacl(t, "/p", []aclStep{{svc, acl.Access}}, []aclStep{{old, acl.Access}}); got != want {
			t.Fatal(got)
		}
	})
}

// Ported from src/resources/acl.test.ts › settling the declaration.
func TestResolveEntries(t *testing.T) {
	t.Run("settles the permissions once rather than at each comparison", func(t *testing.T) {
		got, err := resolveEntries("/mnt/pool/project", []AclEntryArgs{declaredEntry("user", "svc", "rw")}, []AclEntryArgs{declaredEntry("user", "svc", "7")})
		if err != nil || got.Entries[0].Perms != "rw-" || got.DefaultEntries[0].Perms != "rwx" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("fills in an absent name so two spellings of the owning entry compare equal", func(t *testing.T) {
		got, err := resolveEntries("/p", nil, []AclEntryArgs{declaredEntry("other", "", "---")})
		if err != nil || got.DefaultEntries[0].Name != "" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("sorts, so reordering a declaration is not a change", func(t *testing.T) {
		forwards, _ := resolveEntries("/p", []AclEntryArgs{declaredEntry("user", "b", "rwx"), declaredEntry("user", "a", "r-x")}, nil)
		backwards, _ := resolveEntries("/p", []AclEntryArgs{declaredEntry("user", "a", "r-x"), declaredEntry("user", "b", "rwx")}, nil)
		if !reflect.DeepEqual(forwards, backwards) || forwards.Entries[0].Name != "a" {
			t.Fatalf("%+v != %+v", forwards, backwards)
		}
	})
	t.Run("raises a refusal with the path in it, since a declaration names several", func(t *testing.T) {
		_, err := resolveEntries("/mnt/pool/project", []AclEntryArgs{declaredEntry("mask", "", "rwx")}, nil)
		if err == nil || !strings.Contains(err.Error(), "/mnt/pool/project") {
			t.Fatal(err)
		}
	})
	t.Run("treats an empty declaration as empty rather than as nothing", func(t *testing.T) {
		got, err := resolveEntries("/p", nil, nil)
		if err != nil || got.Entries == nil || got.DefaultEntries == nil || len(got.Entries)+len(got.DefaultEntries) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

// Ported from src/resources/acl.test.ts › normaliseRefusal.
func TestNormaliseRefusal(t *testing.T) {
	rule := AclNormaliseArgs{Group: "rwgroup", From: "2755", To: "2775"}
	with := func(f func(*AclNormaliseArgs)) AclNormaliseArgs { r := rule; f(&r); return r }
	t.Run("accepts a rule that changes a mode", func(t *testing.T) {
		if r := normaliseRefusal(rule); r != "" {
			t.Fatal(r)
		}
		if r := normaliseRefusal(with(func(r *AclNormaliseArgs) { r.Ignore = []string{"var", "media/private/"} })); r != "" {
			t.Fatal(r)
		}
	})
	t.Run("refuses a rule with no group, which would match nothing useful", func(t *testing.T) {
		if r := normaliseRefusal(with(func(r *AclNormaliseArgs) { r.Group = " " })); !strings.Contains(r, "group") {
			t.Fatal(r)
		}
	})
	t.Run("refuses a mode that is not a mode", func(t *testing.T) {
		if r := normaliseRefusal(with(func(r *AclNormaliseArgs) { r.From = "rwx" })); !strings.Contains(r, "not a mode") {
			t.Fatal(r)
		}
		if r := normaliseRefusal(with(func(r *AclNormaliseArgs) { r.To = "9999" })); !strings.Contains(r, "not a mode") {
			t.Fatal(r)
		}
	})
	t.Run("refuses a rule that changes nothing, however the two are spelled", func(t *testing.T) {
		if r := normaliseRefusal(AclNormaliseArgs{Group: "g", From: "755", To: "0755"}); !strings.Contains(r, "change nothing") {
			t.Fatal(r)
		}
	})
	t.Run("refuses an ignore entry that could not be under the path", func(t *testing.T) {
		for _, ignore := range []string{"/var", "../var", "a/../b", "./a", "", "/"} {
			if r := normaliseRefusal(with(func(r *AclNormaliseArgs) { r.Ignore = []string{ignore} })); !strings.Contains(r, "relative") {
				t.Errorf("%q: %q", ignore, r)
			}
		}
	})
}

// Ported from src/resources/acl.test.ts › resolveNormalise.
func TestResolveNormalise(t *testing.T) {
	t.Run("settles a rule to one spelling, so a reorder does not walk the tree again", func(t *testing.T) {
		forwards, err1 := resolveNormalise(AclNormaliseArgs{Group: "g", From: "755", To: "775", Ignore: []string{"b", "a/"}})
		backwards, err2 := resolveNormalise(AclNormaliseArgs{Group: "g", From: "0755", To: "0775", Ignore: []string{"a", "b"}})
		want := AclNormaliseArgs{Group: "g", From: "0755", To: "0775", Ignore: []string{"a", "b"}}
		if err1 != nil || err2 != nil || !reflect.DeepEqual(forwards, backwards) || !reflect.DeepEqual(forwards, want) {
			t.Fatalf("%+v %+v", forwards, backwards)
		}
	})
	t.Run("gives an empty ignore list rather than none, so absent and empty compare equal", func(t *testing.T) {
		got, err := resolveNormalise(AclNormaliseArgs{Group: "g", From: "2755", To: "2775"})
		if err != nil || got.Ignore == nil || len(got.Ignore) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("raises the refusal", func(t *testing.T) {
		if _, err := resolveNormalise(AclNormaliseArgs{Group: "g", From: "2775", To: "2775"}); err == nil || !strings.Contains(err.Error(), "change nothing") {
			t.Fatal(err)
		}
	})
}

// Ported from src/resources/acl.test.ts › normaliseCommand.
func TestNormaliseCommand(t *testing.T) {
	rule := AclNormaliseArgs{Group: "rwgroup", From: "2755", To: "2775"}
	ignoring := func(ignore ...string) AclNormaliseArgs { r := rule; r.Ignore = ignore; return r }
	t.Run("matches the exact mode and group, on directories, on one filesystem", func(t *testing.T) {
		want := "find '/mnt/pool' -xdev -type d -group 'rwgroup' -perm 2755 -print -exec chmod 2775 {} + | wc -l"
		if got := normaliseCommand("/mnt/pool", rule); got != want {
			t.Fatal(got)
		}
	})
	t.Run("prunes ignored paths so the walk never enters them", func(t *testing.T) {
		want := `find '/mnt/pool' -xdev \( -path '/mnt/pool/var' -o -path '/mnt/pool/data/k3s' \) -prune -o ` +
			"-type d -group 'rwgroup' -perm 2755 -print -exec chmod 2775 {} + | wc -l"
		if got := normaliseCommand("/mnt/pool", ignoring("var", "data/k3s")); got != want {
			t.Fatal(got)
		}
	})
	t.Run("treats an ignored name literally, since -path takes a pattern", func(t *testing.T) {
		if got := normaliseCommand("/p", ignoring("a[1]*")); !strings.Contains(got, `-path '/p/a\[1\]\*'`) {
			t.Fatal(got)
		}
	})
	t.Run("does not double the slash when the path ends in one", func(t *testing.T) {
		if got := normaliseCommand("/p/", ignoring("a")); !strings.Contains(got, `find '/p' -xdev \( -path '/p/a'`) {
			t.Fatal(got)
		}
	})
	t.Run("never leaves a name able to escape its quoting", func(t *testing.T) {
		if got := normaliseCommand("/p", ignoring("it's")); !strings.Contains(got, `'/p/it'\''s'`) {
			t.Fatal(got)
		}
	})
}

// Ported from src/resources/acl.test.ts › normaliseCheckCommand.
func TestNormaliseCheckCommand(t *testing.T) {
	t.Run("walks exactly what the correction walks, and changes nothing", func(t *testing.T) {
		rule := AclNormaliseArgs{Group: "g", From: "2755", To: "2775", Ignore: []string{"a"}}
		check := normaliseCheckCommand("/p", rule)
		if strings.Contains(check, "chmod") || strings.Replace(normaliseCommand("/p", rule), " -exec chmod 2775 {} +", "", 1) != check {
			t.Fatal(check)
		}
	})
}

// Ported from src/resources/acl.test.ts › normaliseChanged (added by #51; these five cases are not
// listed in #35's checklist).
func TestNormaliseChanged(t *testing.T) {
	rule := AclNormaliseArgs{Group: "rwgroup", From: "2755", To: "2775", Ignore: []string{"k3s", "backups/.snapshots"}}
	changed := func(applied, wanted *AclNormaliseArgs) bool {
		t.Helper()
		got, err := normaliseChanged(applied, wanted)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	resolved := func(r AclNormaliseArgs) *AclNormaliseArgs {
		t.Helper()
		out, err := resolveNormalise(r)
		if err != nil {
			t.Fatal(err)
		}
		return &out
	}
	with := func(f func(*AclNormaliseArgs)) *AclNormaliseArgs { r := rule; f(&r); return &r }
	t.Run("is no change when the state comes back with its keys in the order Pulumi stores them", func(t *testing.T) {
		// Pulumi returns stored objects with their keys sorted; a struct has no key order, and the
		// stored ignore list is the sorted one
		stored := AclNormaliseArgs{From: "2755", Group: "rwgroup", Ignore: []string{"backups/.snapshots", "k3s"}, To: "2775"}
		if changed(&stored, &rule) {
			t.Fatal("changed")
		}
	})
	t.Run("is no change against the rule exactly as it was applied", func(t *testing.T) {
		if changed(resolved(rule), &rule) {
			t.Fatal("changed")
		}
	})
	t.Run("is no change between two spellings of one rule", func(t *testing.T) {
		if changed(&AclNormaliseArgs{Group: "g", From: "755", To: "775", Ignore: []string{"k3s/"}}, &AclNormaliseArgs{Group: "g", From: "0755", To: "0775", Ignore: []string{"k3s"}}) {
			t.Fatal("changed")
		}
	})
	t.Run("notices each field changing", func(t *testing.T) {
		applied := resolved(rule)
		for _, wanted := range []*AclNormaliseArgs{
			with(func(r *AclNormaliseArgs) { r.Group = "other" }),
			with(func(r *AclNormaliseArgs) { r.From = "2750" }),
			with(func(r *AclNormaliseArgs) { r.To = "2770" }),
			with(func(r *AclNormaliseArgs) { r.Ignore = []string{"k3s"} }),
			with(func(r *AclNormaliseArgs) { r.Ignore = []string{"k3s", "backups"} }),
		} {
			if !changed(applied, wanted) {
				t.Errorf("%+v not noticed", *wanted)
			}
		}
	})
	t.Run("notices a rule being added or removed, and not an absent one staying absent", func(t *testing.T) {
		if !changed(nil, &rule) || !changed(resolved(rule), nil) || changed(nil, nil) {
			t.Fatal("wrong")
		}
	})
}

// Entries sort as node's localeCompare sorts them, Unicode names included.
func TestResolveEntriesSortsAsNodeDoes(t *testing.T) {
	got, err := resolveEntries("/p", []AclEntryArgs{
		declaredEntry("user", "f", "r"), declaredEntry("user", "é", "r"), declaredEntry("user", "Alice", "r"),
		declaredEntry("user", "Åke", "r"), declaredEntry("user", "0", "r"), declaredEntry("user", "😀", "r"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range got.Entries {
		names = append(names, e.Name)
	}
	// node: ['user:f','user:é','user:Alice','user:Åke','user:0','user:😀'].sort((a, b) => a.localeCompare(b))
	if want := []string{"😀", "0", "Åke", "Alice", "é", "f"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %q, want %q", names, want)
	}
}
