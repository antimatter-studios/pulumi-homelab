// Package fstab holds the pure fstab editing from src/resources/fstab.ts: finding, writing,
// replacing and removing one entry while leaving every other byte of the file alone.
package fstab

import (
	"regexp"
	"strconv"
	"strings"
)

// Marker is the annotation written on the line above every entry this provider owns.
const Marker = "# pulumi-homelab"

// Entry is the part of an fstab entry that makes up its line.
type Entry struct {
	Source  string
	Target  string
	Type    string
	Options []string
	Dump    int
	Pass    int
}

func isMarker(line string) bool  { return strings.HasPrefix(strings.TrimSpace(line), Marker) }
func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

// Line writes the six fields in fstab's order, with defaults for an empty options list.
func Line(e Entry) string {
	options := "defaults"
	if len(e.Options) > 0 {
		options = strings.Join(e.Options, ",")
	}
	return e.Source + " " + e.Target + " " + e.Type + " " + options + " " + strconv.Itoa(e.Dump) + " " + strconv.Itoa(e.Pass)
}

// TargetOf is the mount point a line describes; ok is false for a comment, a blank line, or
// anything with fewer than three fields.
func TargetOf(line string) (target string, ok bool) {
	if isComment(line) || strings.TrimSpace(line) == "" {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return "", false
	}
	return fields[1], true
}

func indexOf(lines []string, target string) int {
	for i, line := range lines {
		if t, ok := TargetOf(line); ok && t == target {
			return i
		}
	}
	return -1
}

// FindEntry is the uncommented entry for that mount point; ok is false when the file has none.
func FindEntry(text, target string) (line string, ok bool) {
	lines := strings.Split(text, "\n")
	if at := indexOf(lines, target); at >= 0 {
		return lines[at], true
	}
	return "", false
}

var trailingNewlines = regexp.MustCompile(`\n+$`)

// Upsert puts the line in, replacing in place where the target is already described (taking a
// marker above it into account) and appending otherwise, with exactly one trailing newline.
func Upsert(text, target, line string) string {
	lines := strings.Split(text, "\n")
	if at := indexOf(lines, target); at >= 0 {
		from := at
		if at > 0 && isMarker(lines[at-1]) {
			from = at - 1
		}
		out := append([]string{}, lines[:from]...)
		out = append(out, Marker, line)
		out = append(out, lines[at+1:]...)
		return strings.Join(out, "\n")
	}
	body := trailingNewlines.ReplaceAllString(text, "")
	return body + "\n" + Marker + "\n" + line + "\n"
}

// Remove takes the entry out, with its marker, and nothing else.
func Remove(text, target string) string {
	lines := strings.Split(text, "\n")
	at := indexOf(lines, target)
	if at < 0 {
		return text
	}
	from := at
	if at > 0 && isMarker(lines[at-1]) {
		from = at - 1
	}
	out := append([]string{}, lines[:from]...)
	out = append(out, lines[at+1:]...)
	return strings.Join(out, "\n")
}

// MarkedTargets is the mount points of every marked entry in the file, in file order.
func MarkedTargets(text string) []string {
	lines := strings.Split(text, "\n")
	targets := []string{}
	for i, line := range lines {
		if !isMarker(line) || i+1 >= len(lines) {
			continue
		}
		if t, ok := TargetOf(lines[i+1]); ok {
			targets = append(targets, t)
		}
	}
	return targets
}
