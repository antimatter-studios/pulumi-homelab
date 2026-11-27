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
// pattern's first match, or "" when there is none or the pattern does not compile. An empty
// pattern means DefaultVersionPattern.
func ParseVersion(out string, pattern string) string {
	if pattern == "" {
		pattern = DefaultVersionPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ""
	}
	match := re.FindStringSubmatch(out)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}
