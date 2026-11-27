// Package aptmark holds the pure logic from src/aptmark.ts: reading which packages apt holds as
// manually installed, deciding which declared ones it holds as auto, and composing the mark.
package aptmark

import (
	"slices"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/shell"
)

// ParseManual reads `apt-mark showmanual`: one name per line, architecture stripped, blank lines
// and commentary dropped.
func ParseManual(out string) []string {
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.Split(strings.TrimSpace(line), ":")[0]
		if name != "" && !strings.HasPrefix(name, "#") {
			names = append(names, name)
		}
	}
	return names
}

// AutoMarked is the declared packages that are installed (by bare name, whatever architecture)
// but not held as manual, in declared order. Only the keys of installed are read.
func AutoMarked(declared []string, installed map[string]string, manual []string) []string {
	present := map[string]bool{}
	for name := range installed {
		present[strings.Split(name, ":")[0]] = true
	}
	found := []string{}
	for _, name := range declared {
		if present[name] && !slices.Contains(manual, name) {
			found = append(found, name)
		}
	}
	return found
}

// MarkManualCommand marks the names as manually installed in one quiet command, sorted so the
// same set always gives the same string; ok is false when there is nothing to mark.
func MarkManualCommand(names []string) (command string, ok bool) {
	if len(names) == 0 {
		return "", false
	}
	sorted := slices.Clone(names)
	// JavaScript's default sort compares UTF-16 code units; for the ASCII names apt allows that
	// is the same order as Go's byte-wise string comparison
	slices.Sort(sorted)
	quoted := make([]string, len(sorted))
	for i, name := range sorted {
		quoted[i] = shell.Quote(name)
	}
	return "apt-mark manual " + strings.Join(quoted, " ") + " >/dev/null", true
}
