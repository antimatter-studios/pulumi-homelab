// Package swap holds the pure parsers from src/resources/swap.ts: what /proc/swaps says is
// swapping now, and which fstab lines will bring swap back at the next boot.
package swap

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// Active is one line of /proc/swaps.
type Active struct {
	Path string
	// Kind is `file` or `partition`, as /proc/swaps spells it.
	Kind   string
	SizeMB int
	// Priority is a float64 because the TypeScript reads it with Number() and keeps the line
	// whatever it gives, NaN included.
	Priority float64
}

// jsNumber converts text the way JavaScript's Number() does for the shapes /proc/swaps can hold:
// blank is 0, a 0x/0o/0b prefix is read in that base, anything else must be a decimal literal.
// It returns NaN where Number() would.
func jsNumber(text string) float64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if len(text) > 2 && text[0] == '0' {
		base := 0
		switch text[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, err := strconv.ParseUint(text[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	for _, r := range text {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return math.NaN()
		}
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return n // out of range is ±Inf or 0, as in JavaScript
}

// ParseProcSwaps reads /proc/swaps: the header skipped, sizes from kilobytes rounded to MB (half
// up, as Math.round does), and any line whose size is not a finite number dropped.
func ParseProcSwaps(out string) []Active {
	lines := strings.Split(out, "\n")[1:]
	swaps := []Active{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		priority := "0"
		if len(fields) >= 5 {
			priority = fields[4]
		}
		fields = append(fields, "", "", "")
		size := math.Floor(jsNumber(fields[2])/1024 + 0.5)
		if fields[0] == "" || math.IsNaN(size) || math.IsInf(size, 0) {
			continue
		}
		swaps = append(swaps, Active{Path: fields[0], Kind: fields[1], SizeMB: int(size), Priority: jsNumber(priority)})
	}
	return swaps
}

// ParseFstabSwap is the uncommented fstab lines, trimmed, whose third field is `swap`.
func ParseFstabSwap(out string) []string {
	found := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fields := strings.Fields(line); len(fields) > 2 && fields[2] == "swap" {
			found = append(found, line)
		}
	}
	return found
}

// FstabLinesFor is the lines, already kept by ParseFstabSwap, whose source field is exactly path.
func FstabLinesFor(lines []string, path string) []string {
	found := []string{}
	for _, line := range lines {
		if firstField(line) == path {
			found = append(found, line)
		}
	}
	return found
}

// firstField is line.split(/\s+/)[0]: the text before the first whitespace, which is empty for a
// line that starts with whitespace.
func firstField(line string) string {
	if i := strings.IndexFunc(line, func(r rune) bool { return strings.ContainsRune(" \t\n\v\f\r", r) }); i >= 0 {
		return line[:i]
	}
	return line
}
