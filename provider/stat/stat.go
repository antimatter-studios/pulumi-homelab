// Package stat holds the pure stat parsers from src/resources/directory.ts and
// src/resources/file.ts, which read a path's kind, mode, owner and group back from the machine.
package stat

import (
	"strings"
	"unicode"

	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
)

func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }

// jsTrim is JavaScript's String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// Stat is what `stat -c '%F|%a|%U|%G'` says about a path. A field the output did not have is "".
type Stat struct {
	Kind  string
	Mode  string
	Owner string
	Group string
}

// FileStat is what `stat -c '%a %U %G'` says about a file. A field the output did not have is "".
type FileStat struct {
	Mode  string
	Owner string
	Group string
}

func field(fields []string, at int) string {
	if at < len(fields) {
		return fields[at]
	}
	return ""
}

// ParseStat reads `kind|mode|owner|group`, with the mode normalised to four digits.
func ParseStat(out string) Stat {
	fields := strings.Split(jsTrim(out), "|")
	return Stat{Kind: field(fields, 0), Mode: mode.NormaliseMode(field(fields, 1)), Owner: field(fields, 2), Group: field(fields, 3)}
}

// ParseFileStat reads `mode owner group`, split on whitespace, with the mode normalised to four
// digits; empty strings, not an error, for empty output.
func ParseFileStat(out string) FileStat {
	fields := strings.FieldsFunc(out, isJSSpace)
	return FileStat{Mode: mode.NormaliseMode(field(fields, 0)), Owner: field(fields, 1), Group: field(fields, 2)}
}
