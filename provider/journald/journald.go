// Package journald holds the pure rendering and parsing for the Journald resource, ported from
// src/resources/journald.ts.
package journald

import (
	"sort"
	"strings"
	"unicode"
)

// jsSpace is JavaScript's whitespace (trim, \s): Go's unicode.IsSpace plus U+FEFF, minus U+0085,
// which Go counts as space and JavaScript does not.
func jsSpace(r rune) bool { return (unicode.IsSpace(r) || r == 0xFEFF) && r != 0x85 }

// jsTrim is JavaScript's String.prototype.trim.
func jsTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

// JournaldFile renders the drop-in: a header saying who wrote it, a [Journal] section, one line per
// setting in key order, and a trailing newline.
//
// Key order is what the TypeScript actually wrote. Its journaldFile renders Object.entries in
// insertion order, but the settings reach a dynamic provider as a resource input, through a
// protobuf Struct whose keys arrive sorted, so every drop-in on a machine today has its settings
// sorted by key, whatever order the program declared them in. Rendering in any other order would
// rewrite every one of them at the switch.
func JournaldFile(settings map[string]string) string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + settings[k]
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
