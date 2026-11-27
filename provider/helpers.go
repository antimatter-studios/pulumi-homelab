package homelab

import (
	"fmt"
	"regexp"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
)

// heredoc is src/ssh.ts heredoc: write content to a literal path through a quoted here-document.
func heredoc(path, content string, after ...string) string {
	return heredocInto(shellQuote(path), content, after...)
}

// heredocInto is src/ssh.ts heredocInto: the same, for a destination that is a shell expression.
// Anything that must run after the write is chained on the command line, before the body, because
// the terminator has to stand alone on its line.
func heredocInto(target, content string, after ...string) string {
	const edge = "PULUMI_EOF"
	body := content
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	chain := ""
	if len(after) > 0 {
		chain = " && " + strings.Join(after, " && ")
	}
	return "cat > " + target + " <<'" + edge + "'" + chain + "\n" + body + edge + "\n"
}

var lastSegment = regexp.MustCompile(`/[^/]*$`)

// parentOf is the TypeScript's `path.replace(/\/[^/]*$/, ”) || '/'`.
func parentOf(path string) string {
	if d := lastSegment.ReplaceAllString(path, ""); d != "" {
		return d
	}
	return "/"
}

// jsonString is JSON.stringify of a string: quote, backslash and control characters escaped as
// JavaScript escapes them, and everything else, U+2028 and U+2029 included, written as it is.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// joined is Array.prototype.join(',').
func joined(list []string) string { return strings.Join(list, ",") }

// diffOf builds a DiffResponse: changed says whether anything differs, replaces names the
// properties whose change is a replacement.
func diffOf(changed bool, deleteBeforeReplace bool, replaces ...string) p.DiffResponse {
	resp := p.DiffResponse{
		HasChanges:          changed || len(replaces) > 0,
		DeleteBeforeReplace: deleteBeforeReplace,
		DetailedDiff:        map[string]p.PropertyDiff{},
	}
	for _, r := range replaces {
		resp.DetailedDiff[r] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	return resp
}

// splitOnce is `text.split(sep)` keeping only what is before the first separator and what is
// between the first and second, as a destructuring `[a = ”, b = ”] = text.split(sep)` does.
func splitOnce(text, sep string) (string, string) {
	parts := strings.Split(text, sep)
	a, b := parts[0], ""
	if len(parts) > 1 {
		b = parts[1]
	}
	return a, b
}

// firstLineSplit is `text.indexOf('\n')` then slice(0, i) and slice(i + 1); with no newline JS's
// slice(0, -1) drops the last character and slice(0) is the whole text.
func firstLineSplit(text string) (string, string) {
	i := strings.Index(text, "\n")
	if i < 0 {
		if text == "" {
			return "", ""
		}
		return text[:len(text)-1], text
	}
	return text[:i], text[i+1:]
}
