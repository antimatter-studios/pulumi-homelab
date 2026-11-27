// Package systemd holds the pure parsing and naming for the SystemdUnit and SystemdInstance
// resources, ported from src/resources/systemd.ts and src/resources/instance.ts.
package systemd

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// jsTrim is JavaScript's String.prototype.trim.
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
}

// ParseShow reads `systemctl show -p …` output by key, whatever order systemd returns the
// properties in. Lines that are not key=value are ignored; an = inside a value is kept.
func ParseShow(out string) map[string]string {
	shown := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		equals := strings.Index(line, "=")
		if equals <= 0 {
			continue
		}
		shown[jsTrim(line[:equals])] = jsTrim(line[equals+1:])
	}
	return shown
}

func allowed(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
		r == ':' || r == '_' || r == '.'
}

// EscapeInstance escapes an instance name the way systemd-escape does: [A-Za-z0-9:_.] pass
// through, / becomes -, anything else becomes \xNN for each of its UTF-8 bytes, and a leading dot
// is escaped so the unit is not a hidden file.
func EscapeInstance(instance string) string {
	var b strings.Builder
	for _, r := range instance {
		switch {
		case allowed(r):
			b.WriteRune(r)
		case r == '/':
			b.WriteByte('-')
		default:
			// invalid UTF-8 arrives here as U+FFFD, which is also what TextEncoder makes of a lone
			// surrogate in the TypeScript
			var buf [utf8.UTFMax]byte
			n := utf8.EncodeRune(buf[:], r)
			for _, by := range buf[:n] {
				fmt.Fprintf(&b, `\x%02x`, by)
			}
		}
	}
	escaped := b.String()
	if strings.HasPrefix(escaped, ".") {
		return `\x2e` + escaped[1:]
	}
	return escaped
}
