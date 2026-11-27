// Package audit holds the pure parsers from src/audit.ts: splitting the audit's one reply into
// its sections and reading the packages, install times, units and accounts out of them.
package audit

import (
	"regexp"
	"strconv"
	"strings"
)

const mark = "#pulumi-homelab#"

// Sections is the audit's reply split under its markers. Every field is an empty, non-nil list
// when the machine answered nothing for it.
type Sections struct {
	Packages  []string
	Times     []string
	Units     []string
	Passwd    []string
	Fstab     []string
	LoginDefs []string
}

// SplitSections puts every non-blank line under the marker above it. Lines before the first
// marker, or under a marker it does not know, are dropped.
func SplitSections(out string) Sections {
	s := Sections{Packages: []string{}, Times: []string{}, Units: []string{}, Passwd: []string{}, Fstab: []string{}, LoginDefs: []string{}}
	byName := map[string]*[]string{
		"packages": &s.Packages, "times": &s.Times, "units": &s.Units,
		"passwd": &s.Passwd, "fstab": &s.Fstab, "logindefs": &s.LoginDefs,
	}
	var current *[]string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, mark) {
			current = byName[strings.TrimSpace(line[len(mark):])]
			continue
		}
		if current != nil && strings.TrimSpace(line) != "" {
			*current = append(*current, line)
		}
	}
	return s
}

// jsNumber converts text the way JavaScript's Number() does for the shapes these parsers meet: an
// empty or blank string is 0, a 0x/0o/0b prefix is read in that base, and anything else must be
// a whole decimal literal. ok is false where Number() would give NaN or an infinity.
func jsNumber(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, true
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
			return float64(n), err == nil
		}
	}
	for _, r := range text {
		if !strings.ContainsRune("0123456789+-.eE", r) {
			return 0, false
		}
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

var listSuffix = regexp.MustCompile(`\.list$`)

// ParseInstallTimes reads `stat -c '%Y %n'` lines for dpkg's file lists into the time each
// package's list was last written, by bare package name, keeping the later of two architectures.
func ParseInstallTimes(lines []string) map[string]float64 {
	times := map[string]float64{}
	for _, line := range lines {
		space := strings.Index(line, " ")
		if space < 0 {
			continue
		}
		when, ok := jsNumber(line[:space])
		if !ok {
			continue
		}
		file := line[space+1:]
		base := listSuffix.ReplaceAllString(file[strings.LastIndex(file, "/")+1:], "")
		name := strings.Split(base, ":")[0]
		if already, seen := times[name]; !seen || when > already {
			times[name] = when
		}
	}
	return times
}

var serviceSuffix = regexp.MustCompile(`\.service$`)

// ParseUnitNames reads service paths into unit names without the suffix, as SystemdUnit takes
// them, skipping anything that is not a service and an unexpanded pattern.
func ParseUnitNames(lines []string) []string {
	names := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ".service") || strings.Contains(line, "*") {
			continue
		}
		names = append(names, serviceSuffix.ReplaceAllString(line[strings.LastIndex(line, "/")+1:], ""))
	}
	return names
}

// UserFinding is a login account read from passwd.
type UserFinding struct {
	Name  string
	UID   int
	Home  string
	Shell string
}

// ParseLoginUsers keeps the passwd entries whose uid is at least uidMin and below 65534.
func ParseLoginUsers(lines []string, uidMin float64) []UserFinding {
	found := []UserFinding{}
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, ok := jsNumber(fields[2])
		if !ok || uid < uidMin || uid >= 65534 {
			continue
		}
		found = append(found, UserFinding{Name: fields[0], UID: int(uid), Home: fields[5], Shell: fields[6]})
	}
	return found
}

var uidMinPrefix = regexp.MustCompile(`^UID_MIN\s+`)

// ParseUIDMin reads UID_MIN out of login.defs lines, or 1000 when none gives a positive number.
func ParseUIDMin(lines []string) float64 {
	for _, line := range lines {
		if value, ok := jsNumber(uidMinPrefix.ReplaceAllString(line, "")); ok && value > 0 {
			return value
		}
	}
	return 1000
}
