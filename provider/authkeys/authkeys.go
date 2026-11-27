// Package authkeys holds the pure authorized_keys handling from src/resources/authorizedkey.ts:
// identifying a key by its base64 body, writing its line, and putting it in or taking it out of
// a file that other owners share.
package authkeys

import (
	"regexp"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

var bodyPattern = regexp.MustCompile(`(?:^|` + jscompat.SpaceClass + `)(AAAA[0-9A-Za-z+/=]{20,})(?:` + jscompat.SpaceClass + `|$)`)

// KeyBody is the base64 body of a public key, the only part that identifies it. It is found by
// shape rather than by position, since options may contain spaces inside quotes. ok is false for
// a comment, a blank line or anything without a body.
func KeyBody(line string) (body string, ok bool) {
	text := jscompat.Trim(line)
	if text == "" || strings.HasPrefix(text, "#") {
		return "", false
	}
	found := bodyPattern.FindStringSubmatch(text)
	if found == nil {
		return "", false
	}
	return found[1], true
}

// KeyComment is the comment after the body, or "" when there is none.
func KeyComment(line string) string {
	body, ok := KeyBody(line)
	if !ok {
		return ""
	}
	text := jscompat.Trim(line)
	return jscompat.Trim(text[strings.Index(text, body)+len(body):])
}

// AuthorizedLine is the line as it should appear, options first and comma-separated.
func AuthorizedLine(key string, options []string) string {
	restrictions := ""
	if len(options) > 0 {
		restrictions = strings.Join(options, ",") + " "
	}
	return restrictions + jscompat.Trim(key)
}

var trailingNewlines = regexp.MustCompile(`\n+$`)

// UpsertAuthorized puts the line in, replacing whatever line carries the same key body, or
// appending it with one trailing newline.
func UpsertAuthorized(text, key, line string) string {
	lines := strings.Split(text, "\n")
	if body, ok := KeyBody(key); ok {
		for i, existing := range lines {
			if b, ok := KeyBody(existing); ok && b == body {
				lines[i] = line
				return strings.Join(lines, "\n")
			}
		}
	}
	kept := trailingNewlines.ReplaceAllString(text, "")
	if kept != "" {
		return kept + "\n" + line + "\n"
	}
	return line + "\n"
}

// RemoveAuthorized takes out every line carrying this key and leaves every other line alone.
func RemoveAuthorized(text, key string) string {
	body, ok := KeyBody(key)
	if !ok {
		return text
	}
	kept := []string{}
	for _, line := range strings.Split(text, "\n") {
		if b, ok := KeyBody(line); ok && b == body {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
