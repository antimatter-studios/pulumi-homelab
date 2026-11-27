// Package swap holds the pure parsers from src/resources/swap.ts: what /proc/swaps says is
// swapping now, and which fstab lines will bring swap back at the next boot.
package swap

import (
	"math"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
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

// ParseProcSwaps reads /proc/swaps: the header skipped, sizes from kilobytes rounded to MB (half
// up, as Math.round does), and any line whose size is not a finite number dropped.
func ParseProcSwaps(out string) []Active {
	lines := strings.Split(out, "\n")[1:]
	swaps := []Active{}
	for _, line := range lines {
		line = jscompat.Trim(line)
		if line == "" {
			continue
		}
		fields := jscompat.Fields(line)
		priority := "0"
		if len(fields) >= 5 {
			priority = fields[4]
		}
		fields = append(fields, "", "", "")
		size := jscompat.Round(jscompat.Number(fields[2]) / 1024)
		if fields[0] == "" || math.IsNaN(size) || math.IsInf(size, 0) {
			continue
		}
		swaps = append(swaps, Active{Path: fields[0], Kind: fields[1], SizeMB: int(size), Priority: jscompat.Number(priority)})
	}
	return swaps
}

// ParseFstabSwap is the uncommented fstab lines, trimmed, whose third field is `swap`.
func ParseFstabSwap(out string) []string {
	found := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = jscompat.Trim(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if fields := jscompat.Fields(line); len(fields) > 2 && fields[2] == "swap" {
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

// firstField is line.split(/\s+/)[0]: the text before the first JavaScript whitespace, which is
// empty for a line that starts with whitespace.
func firstField(line string) string { return jscompat.Split(line)[0] }
