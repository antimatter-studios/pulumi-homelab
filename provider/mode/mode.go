// Package mode puts file modes into the shape the code writes them in, and reads and folds the
// special bits (setuid, setgid, sticky) carried by a mode's leading digit. Ported from src/mode.ts.
package mode

import (
	"regexp"
	"strings"
	"unicode/utf16"
)

// jsLen is the length JavaScript reports for a string (UTF-16 code units), so the three-digit test
// behaves exactly as the TypeScript did on any input.
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// NormaliseMode gives stat's three-digit answer the leading zero everybody writes, and leaves a
// four-digit mode (a leading zero, or setuid/setgid/sticky bits) untouched.
func NormaliseMode(raw string) string {
	mode := strings.TrimSpace(raw)
	if jsLen(mode) == 3 {
		return "0" + mode
	}
	return mode
}

// SpecialBits are the three bits the high digit of a mode carries, which are not permissions.
type SpecialBits struct {
	Setuid bool
	Setgid bool
	Sticky bool
}

// Flags are special bits as a declaration gives them: nil is a flag nobody wrote, which must not
// overwrite what the mode already says.
type Flags struct {
	Setuid *bool
	Setgid *bool
	Sticky *bool
}

var modePattern = regexp.MustCompile(`^[0-7]{3,4}$`)

// ModeRefusal says why a string is not written the way a mode is, or "" when it is.
func ModeRefusal(mode string) string {
	if modePattern.MatchString(strings.TrimSpace(mode)) {
		return ""
	}
	return mode + " is not a mode: write three or four octal digits, as in '0755' or '2775'"
}

// SpecialBitsOf reads what the high digit of a mode says.
func SpecialBitsOf(mode string) SpecialBits {
	n := NormaliseMode(mode)
	digit := 0
	// Number(x[0] ?? '0') in the TypeScript: a non-digit is NaN, and NaN & bit is 0
	if n != "" && n[0] >= '0' && n[0] <= '9' {
		digit = int(n[0] - '0')
	}
	return SpecialBits{Setuid: digit&4 != 0, Setgid: digit&2 != 0, Sticky: digit&1 != 0}
}

// SpecialBitsRefusal says why a mode and the flags beside it cannot both be right, or "" when they
// can. A non-zero leading digit already answers the question, so any flag beside it is refused.
func SpecialBitsRefusal(mode string, flags Flags) string {
	var given []string
	if flags.Setuid != nil {
		given = append(given, "setuid")
	}
	if flags.Setgid != nil {
		given = append(given, "setgid")
	}
	if flags.Sticky != nil {
		given = append(given, "sticky")
	}
	if len(given) == 0 {
		return ""
	}
	stated := SpecialBitsOf(mode)
	if !stated.Setuid && !stated.Setgid && !stated.Sticky {
		return ""
	}
	verb := "say"
	if len(given) == 1 {
		verb = "says"
	}
	return "mode '" + mode + "' already sets the special bits in its leading digit, and " +
		strings.Join(given, ", ") + " " + verb + " the same thing again. Write one or the other: " +
		"'" + mode + "', or '0" + jsSlice1(NormaliseMode(mode)) + "' with the flags."
}

// WithSpecialBits fills a mode's special bits in from the flags beside it. Always four digits, so
// what is stored and what stat reports are the same string.
func WithSpecialBits(mode string, flags Flags) string {
	normalised := NormaliseMode(mode)
	stated := SpecialBitsOf(normalised)
	pick := func(flag *bool, fallback bool) bool {
		if flag != nil {
			return *flag
		}
		return fallback
	}
	digit := 0
	if pick(flags.Setuid, stated.Setuid) {
		digit |= 4
	}
	if pick(flags.Setgid, stated.Setgid) {
		digit |= 2
	}
	if pick(flags.Sticky, stated.Sticky) {
		digit |= 1
	}
	return string(rune('0'+digit)) + jsSlice1(normalised)
}

// jsSlice1 is JavaScript's s.slice(1): everything after the first UTF-16 code unit.
func jsSlice1(s string) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= 1 {
		return ""
	}
	return string(utf16.Decode(u[1:]))
}
