// Package bootconfig holds the pure config.txt handling of the BootConfig resource from
// src/resources/bootconfig.ts: reading the filter sections, editing lines in one section while
// leaving comments and other sections alone, reading vcgencmd, and rendering the typed settings.
package bootconfig

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// DefaultSection is the implicit section lines before any header belong to.
const DefaultSection = "all"

// jsDot is JavaScript's `.`, which, unlike RE2's, stops at \r, U+2028 and U+2029 as well as \n.
const jsDot = `[^\n\r\x{2028}\x{2029}]`

var (
	headingLine = regexp.MustCompile(`^\[(` + jsDot + `+)\]$`)
	anyHeading  = regexp.MustCompile(`^` + jscompat.SpaceClass + `*\[` + jsDot + `+\]` + jscompat.SpaceClass + `*$`)
)

// renderedAs holds the typed keys the file spells differently.
var renderedAs = map[string]string{"audio": "dtparam=audio"}

// Setting is one key and value. The TypeScript takes an object and writes its entries in
// JavaScript's enumeration order, so the Go takes an ordered list to keep the file byte-identical;
// RenderInput produces that list in the order the live provider wrote.
type Setting struct {
	Key   string
	Value string
}

// ParseBootSections splits config.txt into its filter sections' lines. Lines before any header are
// in the implicit all section, which is always present.
func ParseBootSections(text string) map[string][]string {
	sections := map[string][]string{DefaultSection: {}}
	current := DefaultSection
	for _, line := range strings.Split(text, "\n") {
		if heading := headingLine.FindStringSubmatch(jscompat.Trim(line)); heading != nil {
			current = heading[1]
			if _, ok := sections[current]; !ok {
				sections[current] = []string{}
			}
			continue
		}
		sections[current] = append(sections[current], line)
	}
	return sections
}

// KeyOf is the key a line sets; ok is false for a blank line, a comment or a header.
func KeyOf(line string) (key string, ok bool) {
	text := jscompat.Trim(line)
	if text == "" || strings.HasPrefix(text, "#") || strings.HasPrefix(text, "[") {
		return "", false
	}
	equals := strings.Index(text, "=")
	if equals <= 0 {
		return "", false
	}
	return jscompat.Trim(text[:equals]), true
}

func setsKey(line, key string) bool {
	_, ok := KeyOf(line)
	return ok && strings.HasPrefix(jscompat.Trim(line), key+"=")
}

func carries(lines []string, wanted string) bool {
	for _, line := range lines {
		if jscompat.Trim(line) == jscompat.Trim(wanted) {
			return true
		}
	}
	return false
}

func firstHeader(lines []string) int {
	for i, line := range lines {
		if anyHeading.MatchString(line) {
			return i
		}
	}
	return len(lines)
}

func findHeader(lines []string, section string) int {
	for i, line := range lines {
		if jscompat.Trim(line) == "["+section+"]" {
			return i
		}
	}
	return -1
}

func sectionEnd(lines []string, from int) int {
	end := from
	for end < len(lines) && !anyHeading.MatchString(lines[end]) {
		end++
	}
	return end
}

func join(parts ...[]string) string {
	var out []string
	for _, part := range parts {
		out = append(out, part...)
	}
	return strings.Join(out, "\n")
}

// ApplyToSection puts settings and overlay lines into one section: a setting replaces the line
// setting its key in place or is appended; an overlay line is appended unless the section already
// carries it, and repeated overlays are never collapsed. A section the file does not have is
// appended at the end.
func ApplyToSection(text, section string, settings []Setting, overlays []string) string {
	lines := strings.Split(text, "\n")
	header := findHeader(lines, section)
	end := len(lines)
	if header >= 0 {
		end = sectionEnd(lines, header+1)
	}
	var body []string
	switch {
	case header >= 0:
		body = lines[header+1 : end]
	case section == DefaultSection:
		body = lines[:firstHeader(lines)]
	}

	updated := append([]string{}, body...)
	for _, setting := range settings {
		wanted := setting.Key + "=" + setting.Value
		at := -1
		for i, line := range updated {
			if setsKey(line, setting.Key) {
				at = i
				break
			}
		}
		if at >= 0 {
			updated[at] = wanted
		} else {
			updated = append(updated, wanted)
		}
	}
	for _, overlay := range overlays {
		if !carries(updated, overlay) {
			updated = append(updated, overlay)
		}
	}

	if header >= 0 {
		return join(lines[:header+1], updated, lines[end:])
	}
	if section == DefaultSection {
		return join(updated, lines[firstHeader(lines):])
	}
	return strings.TrimRight(text, "\n") + "\n\n[" + section + "]\n" + strings.Join(updated, "\n") + "\n"
}

// RemoveFromBootSection takes exactly the lines setting these keys, and exactly these overlay
// lines, out of one section, leaving comments behind. Only the keys of settings are used.
func RemoveFromBootSection(text, section string, settings []Setting, overlays []string) string {
	wanted := map[string]bool{}
	for _, overlay := range overlays {
		wanted[jscompat.Trim(overlay)] = true
	}
	lines := strings.Split(text, "\n")
	header := findHeader(lines, section)
	from, end := 0, firstHeader(lines)
	if header >= 0 {
		from = header + 1
		end = sectionEnd(lines, from)
	}
	kept := []string{}
	for _, line := range lines[from:end] {
		named := false
		for _, setting := range settings {
			if setsKey(line, setting.Key) {
				named = true
				break
			}
		}
		if !named && !wanted[jscompat.Trim(line)] {
			kept = append(kept, line)
		}
	}
	return join(lines[:from], kept, lines[end:])
}

// ParseVcgencmd reads `vcgencmd get_config` output into key and value.
func ParseVcgencmd(out string) map[string]string {
	resolved := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		text := jscompat.Trim(line)
		equals := strings.Index(text, "=")
		if equals <= 0 {
			continue
		}
		resolved[text[:equals]] = text[equals+1:]
	}
	return resolved
}

// Typed is one typed setting: a bool, a number or a string; nil means never set (the TypeScript's
// undefined; an explicit null, which Pulumi does not deliver for a declared field, would render as
// "null" there). Ordered, as the TypeScript object's order is the order the lines are written in.
type Typed struct {
	Key   string
	Value any
}

// RenderInput is RenderSettings for settings that arrive as resource inputs, in the order the
// TypeScript provider wrote them. Inputs cross Pulumi's engine as a protobuf Struct whose keys
// arrive sorted, so the TypeScript saw typed keys sorted by their TypeScript name (audio as audio,
// not dtparam=audio) and unchecked keys sorted among themselves: jscompat.SortedKeys for both.
func RenderInput(settings map[string]any, unchecked map[string]string) []Setting {
	typed := make([]Typed, 0, len(settings))
	for _, key := range jscompat.SortedKeys(settings) {
		typed = append(typed, Typed{key, settings[key]})
	}
	extra := make([]Setting, 0, len(unchecked))
	for _, key := range jscompat.SortedKeys(unchecked) {
		extra = append(extra, Setting{key, unchecked[key]})
	}
	return RenderSettings(typed, extra)
}

// RenderSettings renders typed settings into the file's spelling: booleans as 1 and 0, numbers as
// String() writes them (1000000, not 1e+06), audio as dtparam=audio. Unchecked settings come
// last; one naming a key already rendered replaces its value in place, as the TypeScript object
// spread does. The result is in JavaScript's enumeration order for { ...rendered, ...unchecked }:
// array-index keys first, then the rest in the order they were set.
func RenderSettings(settings []Typed, unchecked []Setting) []Setting {
	var rendered []Setting
	set := func(key, value string) {
		for i := range rendered {
			if rendered[i].Key == key {
				rendered[i].Value = value
				return
			}
		}
		rendered = append(rendered, Setting{key, value})
	}
	for _, setting := range settings {
		if setting.Value == nil {
			continue
		}
		key := setting.Key
		if as, ok := renderedAs[key]; ok {
			key = as
		}
		switch value := setting.Value.(type) {
		case bool:
			if value {
				set(key, "1")
			} else {
				set(key, "0")
			}
		default:
			set(key, jsString(value))
		}
	}
	for _, setting := range unchecked {
		set(setting.Key, setting.Value)
	}
	values := map[string]string{}
	keys := make([]string, len(rendered))
	for i, setting := range rendered {
		keys[i], values[setting.Key] = setting.Key, setting.Value
	}
	var ordered []Setting
	for _, key := range jscompat.KeyOrder(keys) {
		ordered = append(ordered, Setting{key, values[key]})
	}
	return ordered
}

// jsString is String(value) for a typed setting's number or string.
func jsString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return jscompat.FormatNumber(v)
	case float32:
		return jscompat.FormatNumber(float64(v))
	case int:
		return jscompat.FormatNumber(float64(v))
	case int64:
		return jscompat.FormatNumber(float64(v))
	case int32:
		return jscompat.FormatNumber(float64(v))
	case uint:
		return jscompat.FormatNumber(float64(v))
	case uint64:
		return jscompat.FormatNumber(float64(v))
	case uint32:
		return jscompat.FormatNumber(float64(v))
	}
	return fmt.Sprint(value)
}
