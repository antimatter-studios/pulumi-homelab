// Package hosts holds the pure logic of the Hostname resource from src/resources/hostname.ts:
// reading and writing the 127.0.1.1 line of /etc/hosts, and splitting the read's one reply.
package hosts

import (
	"sort"
	"strings"
)

const (
	loopback = "127.0.1.1"
	mark     = "#pulumi-homelab#"
)

func firstWord(line string) string {
	words := strings.Fields(line)
	if len(words) == 0 {
		return ""
	}
	return words[0]
}

// HostsNames lists every name on the first 127.0.1.1 line, in the order written.
func HostsNames(text string) []string {
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || words[0] != loopback {
			continue
		}
		return words[1:]
	}
	return []string{}
}

// HostsNamesHost reports whether that line names the host, in any position and in any case; the
// qualified form counts.
func HostsNamesHost(text, name string) bool {
	wanted := strings.ToLower(strings.TrimSpace(name))
	for _, each := range HostsNames(text) {
		found := strings.ToLower(each)
		if found == wanted || strings.HasPrefix(found, wanted+".") {
			return true
		}
	}
	return false
}

// HostsName is the short name the 127.0.1.1 line assigns: the shortest, whichever order they were
// written in (the first of equal length). ok is false when there is no such line.
func HostsName(text string) (name string, ok bool) {
	names := HostsNames(text)
	if len(names) == 0 {
		return "", false
	}
	sorted := append([]string(nil), names...)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i]) < len(sorted[j]) })
	return sorted[0], true
}

// SetHostsName puts the name on the 127.0.1.1 line, replacing it in place, or inserts the line
// after 127.0.0.1 (or at the end) when there is none. Every other line is left alone.
func SetHostsName(text, name, domain string) string {
	wanted := loopback + "\t" + name
	if domain != "" {
		wanted = loopback + "\t" + name + "." + domain + " " + name
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if firstWord(line) == loopback {
			lines[i] = wanted
			return strings.Join(lines, "\n")
		}
	}
	insertAt := len(lines)
	for i, line := range lines {
		if firstWord(line) == "127.0.0.1" {
			insertAt = i + 1
			break
		}
	}
	out := append([]string{}, lines[:insertAt]...)
	out = append(out, wanted)
	out = append(out, lines[insertAt:]...)
	return strings.Join(out, "\n")
}

// HostnameOutput is the read's three answers.
type HostnameOutput struct {
	Static    string
	Transient string
	HostsFile string
}

// ParseHostnameOutput splits the one reply at its two distinct markers.
func ParseHostnameOutput(out string) HostnameOutput {
	parts := strings.Split(out, mark+"now\n")
	fixed, rest := parts[0], ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	parts = strings.Split(rest, mark+"hosts\n")
	now, file := parts[0], ""
	if len(parts) > 1 {
		file = parts[1]
	}
	return HostnameOutput{Static: strings.TrimSpace(fixed), Transient: strings.TrimSpace(now), HostsFile: file}
}
