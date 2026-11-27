// Package getent holds the pure parsing of account reads from src/resources/user.ts. Group's
// getent parsing is ported with the Group resource (provider/group.go) and is not repeated here.
package getent

import (
	"math"
	"slices"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// UserRead is an account as `getent passwd`, `id -nG` and `id -gn` describe it.
type UserRead struct {
	Name string
	// UID is nil where the TypeScript's Number(fields[2]) is NaN (no third field, or not a
	// number) or not a whole number, which a uid never is.
	UID   *int
	Group string
	Home  string
	Shell string
	// Groups is the supplementary groups, sorted, without the primary one.
	Groups []string
}

// ParseUserRead reads the three answers, one per line. The primary group falls back to the
// account's name when `id -gn` said nothing.
func ParseUserRead(name, out string) UserRead {
	lines := append(strings.Split(jscompat.Trim(out), "\n"), "", "")
	passwd, memberships, primary := lines[0], lines[1], lines[2]
	// name:x:uid:gid:gecos:home:shell
	fields := strings.Split(passwd, ":")
	field := func(at int) string {
		if at < len(fields) {
			return fields[at]
		}
		return ""
	}
	group := jscompat.Trim(primary)
	if group == "" {
		group = name
	}
	read := UserRead{Name: name, Group: group, Home: field(5), Shell: field(6), Groups: []string{}}
	if len(fields) > 2 {
		if n := jscompat.Number(fields[2]); n == math.Trunc(n) && !math.IsInf(n, 0) {
			uid := int(n)
			read.UID = &uid
		}
	}
	for _, g := range jscompat.Fields(memberships) {
		if g != group {
			read.Groups = append(read.Groups, g)
		}
	}
	// JavaScript's default sort compares UTF-16 code units
	slices.SortFunc(read.Groups, jscompat.CompareUTF16)
	return read
}
