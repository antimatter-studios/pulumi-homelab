// Package line holds the pure logic of the ManagedLine resource from src/resources/line.ts:
// marking a line, finding it and its anchor again, placing it, reading it back and removing it,
// all without touching any other byte of the file.
package line

import "strings"

// Position is where the line goes: prepend, append, before or after the anchor.
type Position string

const (
	Prepend Position = "prepend"
	Append  Position = "append"
	Before  Position = "before"
	After   Position = "after"
)

// DefaultComment is the comment syntax used for the marker when none is given.
const DefaultComment = "#"

// Args is what the placement functions need. The TypeScript passes structurally typed subsets of
// one object; the Go uses one struct and each function reads only the fields it needs.
type Args struct {
	Line     string
	Marker   string
	Position Position
	Anchor   string
	Comment  string
}

// MarkLine writes the marker into the line as a trailing comment.
func MarkLine(line, marker, comment string) string {
	return line + " " + comment + " " + MarkerOf(marker)
}

// MarkerOf is the text a marked line carries.
func MarkerOf(marker string) string {
	return "pulumi-homelab:" + marker
}

// FindMarked is the index of the first line carrying the marker, or -1.
func FindMarked(text, marker string) int {
	needle := MarkerOf(marker)
	for i, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) {
			return i
		}
	}
	return -1
}

// FindAnchor is the index of the anchor: the last occurrence for after, the first otherwise, and
// -1 when the anchor is empty or absent.
func FindAnchor(text, anchor string, position Position) int {
	if anchor == "" {
		return -1
	}
	lines := strings.Split(text, "\n")
	if position == After {
		for at := len(lines) - 1; at >= 0; at-- {
			if strings.Contains(lines[at], anchor) {
				return at
			}
		}
		return -1
	}
	for i, line := range lines {
		if strings.Contains(line, anchor) {
			return i
		}
	}
	return -1
}

// InsertionPoint is the index a new line is inserted at.
func InsertionPoint(text string, args Args) int {
	lines := strings.Split(text, "\n")
	switch args.Position {
	case Prepend:
		return 0
	case Append:
		return len(lines)
	}
	at := FindAnchor(text, args.Anchor, args.Position)
	if args.Position == Before {
		return at
	}
	return at + 1
}

// CorrectlyPlaced reports whether the marked line is present and on the right side of its anchor.
// A missing anchor is not placed: the requirement can no longer be checked.
func CorrectlyPlaced(text string, args Args) bool {
	at := FindMarked(text, args.Marker)
	if at < 0 {
		return false
	}
	if args.Position == Prepend || args.Position == Append {
		return true
	}
	anchor := FindAnchor(text, args.Anchor, args.Position)
	if anchor < 0 {
		return false
	}
	if args.Position == Before {
		return at < anchor
	}
	return at > anchor
}

// UpsertLine puts the marked line in: replaced in place when it is correctly placed, otherwise
// taken out and inserted at the insertion point.
func UpsertLine(text string, args Args) string {
	wanted := MarkLine(args.Line, args.Marker, args.Comment)
	lines := strings.Split(text, "\n")
	at := FindMarked(text, args.Marker)
	if at >= 0 && CorrectlyPlaced(text, args) {
		lines[at] = wanted
		return strings.Join(lines, "\n")
	}
	without := lines
	if at >= 0 {
		without = append(append([]string{}, lines[:at]...), lines[at+1:]...)
	}
	point := InsertionPoint(strings.Join(without, "\n"), args)
	// before with an anchor the file lacks gives -1, which JavaScript's slice counts from the end:
	// the TypeScript then inserts above the last line. Kept as is; placementRefusal stops it first.
	if point < 0 {
		point = max(point+len(without), 0)
	}
	out := append([]string{}, without[:point]...)
	out = append(out, wanted)
	out = append(out, without[point:]...)
	return strings.Join(out, "\n")
}

// RemoveLine takes out every line carrying the marker, and returns the text unchanged when there
// is none.
func RemoveLine(text, marker, comment string) string {
	prefix := " " + comment + " " + MarkerOf("")
	lines := strings.Split(text, "\n")
	var kept []string
	for _, line := range lines {
		_, found, marked := strings.Cut(strings.TrimSuffix(line, "\r"), prefix)
		if !marked || found != marker {
			kept = append(kept, line)
		}
	}
	if len(kept) == len(lines) {
		return text
	}
	return strings.Join(kept, "\n")
}

// Read is what the file says about the line: the line as the file has it ("" when absent), and
// whether it is correctly placed.
type Read struct {
	Actual string
	Placed bool
}

// ReadLine reports the marked line and whether it is placed.
func ReadLine(text string, args Args) Read {
	at := FindMarked(text, args.Marker)
	actual := ""
	if at >= 0 {
		actual = strings.Split(text, "\n")[at]
	}
	return Read{Actual: actual, Placed: CorrectlyPlaced(text, args)}
}
