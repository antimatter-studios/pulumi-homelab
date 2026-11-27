// Package archive holds the pure logic for the Archive resource, ported from
// src/resources/archive.ts.
package archive

import (
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
// jsPattern): RE2's \s and . are not JavaScript's.
func ParseVersion(out string, pattern string) string {
	re, err := regexp.Compile(jsPattern(pattern))
	if err != nil {
		return ""
	}
	match := re.FindStringSubmatch(out)
	if len(match) < 2 {
		return ""
	}
	return match[1]
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
