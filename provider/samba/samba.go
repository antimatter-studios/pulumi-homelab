// Package samba holds the pure smb.conf handling of the SambaShare, SambaUser and SambaSetting
// resources from src/resources/samba.ts: reading sections and settings, editing one section or one
// setting while leaving every other byte alone, and reading pdbedit and the parse verdict.
package samba

import (
	"regexp"
	"strings"
)

const parsesMarker = "#pulumi-homelab#parses"

// Setting is one key and value, in file order.
type Setting struct {
	Key   string
	Value string
}

// Settings is a section's settings in the order they first appear. The TypeScript uses a plain
// object, whose insertion order decides which of two keys that differ only in spacing a lookup
// finds first and the order a section is written in, so the Go keeps an ordered list.
type Settings []Setting

// Get is the value of exactly this key.
func (s Settings) Get(key string) (string, bool) {
	for _, setting := range s {
		if setting.Key == key {
			return setting.Value, true
		}
	}
	return "", false
}

// set assigns as an object property does: an existing key keeps its position.
func (s Settings) set(key, value string) Settings {
	for i := range s {
		if s[i].Key == key {
			s[i].Value = value
			return s
		}
	}
	return append(s, Setting{key, value})
}

// Map is the settings as a map, for comparisons where order does not matter.
func (s Settings) Map() map[string]string {
	out := make(map[string]string, len(s))
	for _, setting := range s {
		out[setting.Key] = setting.Value
	}
	return out
}

var (
	headingLine = regexp.MustCompile(`^\[(.+)\]$`)
	anyHeading  = regexp.MustCompile(`^\s*\[.+\]\s*$`)
	spaces      = regexp.MustCompile(`\s+`)
)

func sameKey(a, b string) bool {
	norm := func(s string) string { return spaces.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), " ") }
	return norm(a) == norm(b)
}

// ParseSections splits a file into its sections' lines, keyed by name. Lines before the first
// heading belong to no section; a repeated heading adds to the first.
func ParseSections(text string) map[string][]string {
	sections := map[string][]string{}
	current, inSection := "", false
	for _, line := range strings.Split(text, "\n") {
		if heading := headingLine.FindStringSubmatch(strings.TrimSpace(line)); heading != nil {
			current, inSection = heading[1], true
			if _, ok := sections[current]; !ok {
				sections[current] = []string{}
			}
			continue
		}
		if inSection {
			sections[current] = append(sections[current], line)
		}
	}
	return sections
}

// ParseShareSettings reads key = value lines, skipping blanks and # or ; comments; keys keep the
// spaces inside them.
func ParseShareSettings(lines []string) Settings {
	settings := Settings{}
	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
			continue
		}
		equals := strings.Index(text, "=")
		if equals <= 0 {
			continue
		}
		settings = settings.set(strings.TrimSpace(text[:equals]), strings.TrimSpace(text[equals+1:]))
	}
	return settings
}

// EffectiveShare is one section of testparm's output; ok is false when there is no such section.
func EffectiveShare(testparm, share string) (Settings, bool) {
	lines, ok := ParseSections(testparm)[share]
	if !ok {
		return nil, false
	}
	return ParseShareSettings(lines), true
}

// FileShare is one section of smb.conf; ok is false when there is no such section.
func FileShare(text, share string) (Settings, bool) {
	return EffectiveShare(text, share)
}

// ShareSection renders a share's section with path first. A "path" in settings replaces the
// value in that first position, as the TypeScript object spread does.
func ShareSection(share, path string, settings Settings) string {
	body := Settings{{"path", path}}
	for _, setting := range settings {
		body = body.set(setting.Key, setting.Value)
	}
	lines := []string{"[" + share + "]"}
	for _, setting := range body {
		lines = append(lines, "   "+setting.Key+" = "+setting.Value)
	}
	return strings.Join(lines, "\n")
}

// sectionBounds is the heading's index and the index of the next heading (or the end), or -1.
func sectionBounds(lines []string, share string) (start, end int) {
	start = -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "["+share+"]" {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end = start + 1
	for end < len(lines) && !anyHeading.MatchString(lines[end]) {
		end++
	}
	return start, end
}

func join(parts ...[]string) string {
	var out []string
	for _, part := range parts {
		out = append(out, part...)
	}
	return strings.Join(out, "\n")
}

// UpsertSection replaces a section in place, up to the next heading, or appends it after a blank
// line when the file does not have it.
func UpsertSection(text, share, section string) string {
	lines := strings.Split(text, "\n")
	start, end := sectionBounds(lines, share)
	if start < 0 {
		return strings.TrimRight(text, "\n") + "\n\n" + section + "\n"
	}
	return join(lines[:start], strings.Split(section, "\n"), lines[end:])
}

// RemoveSection takes out a section, up to the next heading, and returns the text unchanged when
// it is not there.
func RemoveSection(text, share string) string {
	lines := strings.Split(text, "\n")
	start, end := sectionBounds(lines, share)
	if start < 0 {
		return text
	}
	return join(lines[:start], lines[end:])
}

// ParseSambaUsers takes the names out of `pdbedit -L` output.
func ParseSambaUsers(out string) []string {
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(strings.Split(line, ":")[0]); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func isSettingFor(line, key string) bool {
	text := strings.TrimSpace(line)
	if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, ";") {
		return false
	}
	equals := strings.Index(text, "=")
	return equals > 0 && sameKey(text[:equals], key)
}

// UpsertSetting sets one key in one section, matching the key however it was spaced or
// capitalised and leaving comments alone. A missing section is appended with just this key.
func UpsertSetting(text, share, key, value string) string {
	lines := strings.Split(text, "\n")
	header, end := sectionBounds(lines, share)
	if header < 0 {
		return strings.TrimRight(text, "\n") + "\n\n[" + share + "]\n   " + key + " = " + value + "\n"
	}
	body := append([]string{}, lines[header+1:end]...)
	wanted := "   " + key + " = " + value
	at := -1
	for i, line := range body {
		if isSettingFor(line, key) {
			at = i
			break
		}
	}
	if at >= 0 {
		body[at] = wanted
	} else {
		body = append(body, wanted)
	}
	return join(lines[:header+1], body, lines[end:])
}

// RemoveSetting removes every line setting the key in one section, leaving the section standing.
func RemoveSetting(text, share, key string) string {
	lines := strings.Split(text, "\n")
	header, end := sectionBounds(lines, share)
	if header < 0 {
		return text
	}
	body := []string{}
	for _, line := range lines[header+1 : end] {
		if !isSettingFor(line, key) {
			body = append(body, line)
		}
	}
	return join(lines[:header+1], body, lines[end:])
}

// ShareRead is the section as the file has it (Found false when absent) and whether samba parses
// the file.
type ShareRead struct {
	Actual Settings
	Found  bool
	Parses bool
}

// ParseShareRead splits the read's reply into the file and testparm's verdict.
func ParseShareRead(out, share string) ShareRead {
	parts := strings.Split(out, parsesMarker+"\n")
	text, verdict := parts[0], ""
	if len(parts) > 1 {
		verdict = parts[1]
	}
	actual, found := FileShare(text, share)
	return ShareRead{Actual: actual, Found: found, Parses: strings.TrimSpace(verdict) == "ok"}
}

// FileSetting is one key's value in one section of the file, matched however it was spaced or
// capitalised; ok is false when the section or key is missing.
func FileSetting(text, share, key string) (string, bool) {
	section, ok := FileShare(text, share)
	if !ok {
		return "", false
	}
	for _, setting := range section {
		if sameKey(setting.Key, key) {
			return setting.Value, true
		}
	}
	return "", false
}
