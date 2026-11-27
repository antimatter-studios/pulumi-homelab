// Package journald holds the pure rendering and parsing for the Journald resource, ported from
// src/resources/journald.ts.
package journald

import (
	"strings"
	"unicode"
)

// Setting is one key=value line of the drop-in. The TypeScript renders an object's entries in
// insertion order; Go maps have none, so callers pass the settings in the order to write them.
type Setting struct {
	Key   string
	Value string
}

// jsTrim is JavaScript's String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
}

// JournaldFile renders the drop-in: a header saying who wrote it, a [Journal] section, one line per
// setting, and a trailing newline.
func JournaldFile(settings []Setting) string {
	lines := make([]string, len(settings))
	for i, s := range settings {
		lines[i] = s.Key + "=" + s.Value
	}
	return "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n[Journal]\n" +
		strings.Join(lines, "\n") + "\n"
}

// ParseEffective reads the merged configuration out of what `systemd-analyze cat-config` prints.
// Later assignments win, which is systemd's own rule; comments, section headers and the file-name
// banners are skipped.
func ParseEffective(out string) map[string]string {
	effective := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		text := jsTrim(line)
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") || strings.HasPrefix(text, "[") {
			continue
		}
		equals := strings.Index(text, "=")
		if equals <= 0 {
			continue
		}
		effective[jsTrim(text[:equals])] = jsTrim(text[equals+1:])
	}
	return effective
}
