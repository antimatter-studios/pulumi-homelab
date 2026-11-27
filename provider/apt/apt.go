// Package apt holds the pure parsing for the AptPackage resource, ported from
// src/resources/apt.ts.
package apt

import (
	"strings"
	"unicode"
)

// ParseDpkgStatus reads `dpkg-query -W -f='${db:Status-Status} ${Version}'` output. It returns the
// version and true for an installed package (an empty version when dpkg gives none), and "", false
// for anything else, where the TypeScript returns null: removed, config-files only, or unknown.
func ParseDpkgStatus(out string) (string, bool) {
	words := strings.FieldsFunc(out, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
	if len(words) == 0 || words[0] != "installed" {
		return "", false
	}
	if len(words) < 2 {
		return "", true
	}
	return words[1], true
}
