// Package acl holds the pure logic of the PosixAcl resource from src/resources/acl.ts: settling
// permission spellings, reading getfacl, deciding which entries are satisfied and what to change,
// and formatting entries for setfacl.
package acl

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// EntryType is the kind of ACL entry: user, group, mask or other.
type EntryType string

const (
	User  EntryType = "user"
	Group EntryType = "group"
	Mask  EntryType = "mask"
	Other EntryType = "other"
)

// Scope is whether an entry applies to the path itself (access) or is inherited (default).
type Scope string

const (
	Access  Scope = "access"
	Default Scope = "default"
)

// EntryArgs is a declared entry. An empty Name is the owning entry, as the TypeScript's absent
// name is.
type EntryArgs struct {
	Type  EntryType
	Name  string
	Perms string
}

// Entry is one entry as getfacl reported it: what it says and what the mask leaves of it. An
// absent entry has empty Perms and Effective.
type Entry struct {
	Type      EntryType
	Name      string
	Perms     string
	Effective string
}

var (
	octal = regexp.MustCompile(`^[0-7]$`)
	set   = regexp.MustCompile(`^[rwx-]{1,3}$`)
)

// NormalisePerms settles a permission spelling (rwx, rw, x, -, or an octal digit) on the three
// characters getfacl prints. The conditional X and anything else are refused.
func NormalisePerms(perms string) (string, error) {
	text := jscompat.Trim(perms)
	if octal.MatchString(text) {
		bits := text[0] - '0'
		return flag(bits&4 != 0, 'r') + flag(bits&2 != 0, 'w') + flag(bits&1 != 0, 'x'), nil
	}
	if strings.Contains(text, "X") {
		return "", fmt.Errorf("%s uses setfacl's conditional X, which resolves differently per file and is never "+
			"reported back, so it can only ever read as drift. Write x or - for what this path should have.", perms)
	}
	if !set.MatchString(text) {
		return "", fmt.Errorf("%s is not a permission set: write rwx, r-x, rw or an octal digit", perms)
	}
	return flag(strings.Contains(text, "r"), 'r') + flag(strings.Contains(text, "w"), 'w') + flag(strings.Contains(text, "x"), 'x'), nil
}

func flag(on bool, c byte) string {
	if on {
		return string(c)
	}
	return "-"
}

// ParseGetfacl reads getfacl's output into access and default entries, in the order printed.
// The comment header and blank lines are skipped; an #effective: comment gives what the mask
// leaves, and without one an entry grants what it says.
func ParseGetfacl(out string) (access, defaults []Entry) {
	access, defaults = []Entry{}, []Entry{}
	for _, raw := range strings.Split(out, "\n") {
		line := jscompat.Trim(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "#")
		body, comment := parts[0], ""
		if len(parts) > 1 {
			comment = parts[1]
		}
		fields := strings.Split(jscompat.Trim(body), ":")
		isDefault := fields[0] == "default"
		if isDefault {
			fields = fields[1:]
		}
		if len(fields) < 3 {
			continue
		}
		kind, name, perms := EntryType(fields[0]), fields[1], fields[2]
		if kind != User && kind != Group && kind != Mask && kind != Other {
			continue
		}
		effective := jscompat.Trim(perms)
		if strings.HasPrefix(comment, "effective:") {
			effective = jscompat.Trim(strings.TrimPrefix(comment, "effective:"))
		}
		entry := Entry{Type: kind, Name: name, Perms: jscompat.Trim(perms), Effective: effective}
		if isDefault {
			defaults = append(defaults, entry)
		} else {
			access = append(access, entry)
		}
	}
	return access, defaults
}

// ParseOwnership reads the owner and group from the header getfacl prints without -c ("# owner:"
// and "# group:"), by name as getfacl gives them; "" for a line the output did not have.
func ParseOwnership(out string) (owner, group string) {
	for _, raw := range strings.Split(out, "\n") {
		line := jscompat.Trim(raw)
		if v, ok := strings.CutPrefix(line, "# owner:"); ok {
			owner = jscompat.Trim(v)
		} else if v, ok := strings.CutPrefix(line, "# group:"); ok {
			group = jscompat.Trim(v)
		}
	}
	return owner, group
}

// Named reports whether an entry is for a named user or group, which is what setfacl -x can
// remove; the owning entries and other are always there.
func Named(kind EntryType, name string) bool {
	return (kind == User || kind == Group) && name != ""
}

// EntryKey identifies an entry by type and name.
func EntryKey(kind EntryType, name string) string {
	return string(kind) + ":" + name
}

// FindACLEntry is the reported entry matching the wanted one, or an absent entry (empty Perms and
// Effective), which is not the same as one granting ---.
func FindACLEntry(actual []Entry, wanted EntryArgs) Entry {
	key := EntryKey(wanted.Type, wanted.Name)
	for _, entry := range actual {
		if EntryKey(entry.Type, entry.Name) == key {
			return entry
		}
	}
	return Entry{Type: wanted.Type, Name: wanted.Name}
}

// EntrySatisfied reports whether the entry both says and grants exactly the wanted permissions.
func EntrySatisfied(actual Entry, wanted EntryArgs) (bool, error) {
	perms, err := NormalisePerms(wanted.Perms)
	if err != nil {
		return false, err
	}
	return actual.Perms == perms && actual.Effective == perms, nil
}

// Change is a wanted entry that is not satisfied, with what was found for it.
type Change struct {
	Entry EntryArgs
	Scope Scope
	Found Entry
}

// Unsatisfied lists the wanted entries the machine does not agree about, in declared order.
func Unsatisfied(actual []Entry, wanted []EntryArgs, scope Scope) ([]Change, error) {
	changes := []Change{}
	for _, entry := range wanted {
		found := FindACLEntry(actual, entry)
		ok, err := EntrySatisfied(found, entry)
		if err != nil {
			return nil, err
		}
		if !ok {
			changes = append(changes, Change{Entry: entry, Scope: scope, Found: found})
		}
	}
	return changes, nil
}

// Abandoned lists the previously declared entries no longer declared, by key: an entry whose
// permissions merely changed is not abandoned.
func Abandoned(previous, wanted []EntryArgs) []EntryArgs {
	keys := map[string]bool{}
	for _, entry := range wanted {
		keys[EntryKey(entry.Type, entry.Name)] = true
	}
	gone := []EntryArgs{}
	for _, entry := range previous {
		if !keys[EntryKey(entry.Type, entry.Name)] {
			gone = append(gone, entry)
		}
	}
	return gone
}

func prefix(scope Scope) string {
	if scope == Default {
		return "default:"
	}
	return ""
}

// FormatEntry writes an entry as setfacl takes it, with its permissions settled.
func FormatEntry(entry EntryArgs, scope Scope) (string, error) {
	perms, err := NormalisePerms(entry.Perms)
	if err != nil {
		return "", err
	}
	return prefix(scope) + string(entry.Type) + ":" + entry.Name + ":" + perms, nil
}

// FormatRemoval writes an entry as setfacl -x takes it: no permissions.
func FormatRemoval(kind EntryType, name string, scope Scope) string {
	return prefix(scope) + string(kind) + ":" + name
}
