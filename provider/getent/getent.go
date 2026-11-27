// Package getent holds the pure parsing of account reads from src/resources/user.ts. Group's
// getent parsing is ported with the Group resource (provider/group.go) and is not repeated here.
package getent

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

func isJSSpace(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF }

// jsTrim is JavaScript's String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// jsNumber converts text the way JavaScript's Number() does for the shapes a uid field holds:
// blank is 0, a 0x/0o/0b prefix is read in that base, anything else must be a decimal literal.
// ok is false where Number() gives NaN.
func jsNumber(text string) (float64, bool) {
	text = jsTrim(text)
	if text == "" {
		return 0, true
	}
	if len(text) > 2 && text[0] == '0' {
		base := 0
		switch text[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(text[2:], base, 64)
			return float64(n), err == nil
		}
	}
	for _, r := range text {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return 0, false
		}
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return n, true
}

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
	lines := append(strings.Split(jsTrim(out), "\n"), "", "")
	passwd, memberships, primary := lines[0], lines[1], lines[2]
	// name:x:uid:gid:gecos:home:shell
	fields := strings.Split(passwd, ":")
	field := func(at int) string {
		if at < len(fields) {
			return fields[at]
		}
		return ""
	}
	group := jsTrim(primary)
	if group == "" {
		group = name
	}
	read := UserRead{Name: name, Group: group, Home: field(5), Shell: field(6), Groups: []string{}}
	if len(fields) > 2 {
		if n, ok := jsNumber(fields[2]); ok && n == math.Trunc(n) && !math.IsInf(n, 0) {
			uid := int(n)
			read.UID = &uid
		}
	}
	for _, g := range strings.FieldsFunc(memberships, isJSSpace) {
		if g != group {
			read.Groups = append(read.Groups, g)
		}
	}
	// JavaScript's default sort compares UTF-16 code units, the same order as Go's byte-wise
	// comparison for the ASCII names groups have
	slices.Sort(read.Groups)
	return read
}
