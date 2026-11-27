// Package archive holds the pure logic for the Archive resource, ported from
// src/resources/archive.ts.
package archive

import (
	"fmt"
	"regexp"
	"strings"
)

// Format is a kind of archive the resource can unpack.
type Format string

const (
	TarGz  Format = "tar.gz"
	TarXz  Format = "tar.xz"
	TarBz2 Format = "tar.bz2"
	Zip    Format = "zip"
)

// DefaultVersionPattern is the TypeScript's default version pattern, unchanged.
const DefaultVersionPattern = `([0-9]+\.[0-9]+(?:\.[0-9]+)?[^\s]*)`

var formats = []struct {
	pattern *regexp.Regexp
	format  Format
}{
	{regexp.MustCompile(`(?i)\.(tar\.gz|tgz)$`), TarGz},
	{regexp.MustCompile(`(?i)\.(tar\.xz|txz)$`), TarXz},
	{regexp.MustCompile(`(?i)\.(tar\.bz2|tbz2?)$`), TarBz2},
	{regexp.MustCompile(`(?i)\.zip$`), Zip},
}

// FormatOf says what kind of archive a url names, looking past the query string, or "" when the
// name carries no extension it knows (null in the TypeScript).
func FormatOf(url string) Format {
	path, _, _ := strings.Cut(url, "?")
	for _, f := range formats {
		if f.pattern.MatchString(path) {
			return f.format
		}
	}
	return ""
}

// ParseVersion reads a version out of whatever the software printed: the first capture group of
// pattern's first match, or "" when there is none or the pattern does not compile. The pattern is
// used as given, as the TypeScript's parseVersion does; an empty pattern matches the empty string
// with no group and so gives "". Defaulting to DefaultVersionPattern is the caller's job, for an
// undeclared pattern only, as resolveArgs does with ??.
//
// The pattern is a JavaScript regular expression, so it is translated to Go's syntax first (see
// jsPattern): RE2's \s and . are not JavaScript's. The text is matched as JavaScript sees it, as
// UTF-16 code units (see utf16Units): an unflagged JavaScript RegExp reads a character outside the
// Basic Multilingual Plane, such as an emoji, as two surrogates, so `^.([0-9]+)$` does not match
// "😀42" there, and must not here.
func ParseVersion(out string, pattern string) string {
	re, err := regexp.Compile(jsPattern(utf16Units(pattern)))
	if err != nil {
		return ""
	}
	match := re.FindStringSubmatch(utf16Units(out))
	if len(match) < 2 {
		return ""
	}
	return fromUTF16Units(match[1])
}

// Surrogates cannot be written in a Go string, so utf16Units stands each one of a character
// outside the Basic Multilingual Plane in for a code point of Supplementary Private Use Area-A: the
// high surrogate D800+n as F0000+n, the low DC00+n as F0400+n. Every character outside the plane in
// the text is replaced this way, so no stand-in can be confused with a character the text really
// holds, and every other character, private-use ones included, is matched as itself.
const (
	highBase = 0xF0000
	lowBase  = 0xF0400
)

func utf16Units(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x10000 {
			b.WriteRune(r)
			continue
		}
		r -= 0x10000
		b.WriteRune(highBase + (r >> 10))
		b.WriteRune(lowBase + (r & 0x3FF))
	}
	return b.String()
}

// fromUTF16Units turns utf16Units back into text. A capture can split a pair, which JavaScript
// would return as a lone surrogate; Go has no such string, and it becomes U+FFFD.
func fromUTF16Units(s string) string {
	units := []rune(s)
	var b strings.Builder
	for i := 0; i < len(units); i++ {
		r := units[i]
		switch {
		case r >= highBase && r < lowBase && i+1 < len(units) && units[i+1] >= lowBase && units[i+1] < lowBase+0x400:
			b.WriteRune(0x10000 + (r-highBase)<<10 + (units[i+1] - lowBase))
			i++
		case r >= highBase && r < lowBase+0x400:
			b.WriteRune(0xFFFD)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// jsWhitespace is the contents of JavaScript's \s class.
const jsWhitespace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// jsPattern rewrites the parts of a JavaScript pattern whose meaning differs in RE2, so a pattern
// written for the TypeScript library matches the same text here:
//
//   - \s and \S: RE2's are ASCII only; JavaScript's include \v, NBSP, the Unicode spaces and U+FEFF.
//     Inside a class \s becomes the same characters; \S inside a class has no RE2 spelling and is
//     left as RE2's, which differs only for those non-ASCII spaces.
//   - . outside a class: RE2's excludes only \n; JavaScript's also excludes \r, U+2028 and U+2029.
//
// Everything else is copied. Features RE2 lacks (lookaround, backreferences) still fail to
// compile, and ParseVersion then answers "", as it does for a pattern that does not compile at all.
func jsPattern(p string) string {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			next := p[i+1]
			i++
			switch {
			case next == 's' && inClass:
				b.WriteString(jsWhitespace)
			case next == 's':
				b.WriteString("[" + jsWhitespace + "]")
			case next == 'S' && !inClass:
				b.WriteString("[^" + jsWhitespace + "]")
			default:
				b.WriteByte('\\')
				b.WriteByte(next)
			}
		// JavaScript closes a class at the first ], even straight after [ or [^, so [] matches
		// nothing and [^] matches anything; RE2 would read that ] as a literal instead.
		case c == '[' && !inClass && strings.HasPrefix(p[i:], "[]"):
			b.WriteString(`[^\x00-\x{10FFFF}]`)
			i++
		case c == '[' && !inClass && strings.HasPrefix(p[i:], "[^]"):
			b.WriteString(`[\x00-\x{10FFFF}]`)
			i += 2
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
			if i+1 < len(p) && p[i+1] == '^' {
				b.WriteByte('^')
				i++
			}
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		case c == '.' && !inClass:
			b.WriteString(`[^\n\r\x{2028}\x{2029}]`)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// PatternRefusal reports why RE2 could not compile a versionPattern.
func PatternRefusal(pattern string) string {
	if _, err := regexp.Compile(jsPattern(utf16Units(pattern))); err != nil {
		return fmt.Sprintf("versionPattern %q does not compile as RE2, which the provider reads it with (%v). "+
			"Lookaround and backreferences have no RE2 equivalent; write the pattern without them, or leave "+
			"versionPattern unset for the default.", pattern, err)
	}
	return ""
}
