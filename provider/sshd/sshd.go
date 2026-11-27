// Package sshd holds the pure logic of the SshdConfig resource from src/resources/sshd.ts: reading
// what `sshd -T` resolved, rendering the typed settings as sshd's own keywords, writing the
// drop-in and checking sshd_config includes it.
package sshd

import (
	"fmt"
	"strings"
)

// Directory is the drop-in directory sshd_config is expected to include.
const Directory = "/etc/ssh/sshd_config.d"

// keywords holds the properties sshd does not spell by simple capitalisation.
var keywords = map[string]string{
	"usePAM":        "UsePAM",
	"useDNS":        "UseDNS",
	"x11Forwarding": "X11Forwarding",
	"macs":          "MACs",
	"kexAlgorithms": "KexAlgorithms",
}

// KeywordFor turns a property into the keyword sshd uses: passwordAuthentication becomes
// PasswordAuthentication, apart from the few in the table above.
func KeywordFor(property string) string {
	if keyword, ok := keywords[property]; ok {
		return keyword
	}
	if property == "" {
		return ""
	}
	return strings.ToUpper(property[:1]) + property[1:]
}

// Setting is one typed property and its value. The value is a bool, an int, a string or a
// []string; nil means never set. The renderers take an ordered list and write it in that order,
// which is what the TypeScript's own tests exercise. In production the TypeScript never saw the
// declared order: settings reach a dynamic provider as resource inputs through a protobuf Struct,
// whose keys arrive sorted, so the drop-ins on machines list the typed settings sorted by property
// name, then new unchecked keywords sorted among themselves (one that repeats a typed keyword keeps
// the typed one's place), then Match criteria sorted by key. The SshdConfig resource (#45) must
// pass its lists in that order, or every drop-in changes at the switch.
type Setting struct {
	Property string
	Value    any
}

// Keyword is one rendered keyword and its value, in file order.
type Keyword struct {
	Keyword string
	Value   string
}

// RenderSshd renders the typed settings as sshd's keywords and values: booleans as yes and no,
// lists comma-separated, numbers as themselves. Unchecked keywords come after; one that names a
// keyword already rendered replaces its value in place, as the TypeScript object spread does.
func RenderSshd(settings []Setting, unchecked []Keyword) []Keyword {
	var rendered []Keyword
	set := func(keyword, value string) {
		for i := range rendered {
			if rendered[i].Keyword == keyword {
				rendered[i].Value = value
				return
			}
		}
		rendered = append(rendered, Keyword{keyword, value})
	}
	for _, setting := range settings {
		if setting.Value == nil {
			continue
		}
		keyword := KeywordFor(setting.Property)
		switch value := setting.Value.(type) {
		case bool:
			if value {
				set(keyword, "yes")
			} else {
				set(keyword, "no")
			}
		case []string:
			set(keyword, strings.Join(value, ","))
		default:
			set(keyword, fmt.Sprint(value))
		}
	}
	for _, keyword := range unchecked {
		set(keyword.Keyword, keyword.Value)
	}
	return rendered
}

// ParseSshdT reads `sshd -T` into lowercased keywords and normalised values. Empty output, which
// is what a machine whose config does not parse gives, is an empty map.
func ParseSshdT(out string) map[string]string {
	effective := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		space := strings.Index(text, " ")
		if space <= 0 {
			continue
		}
		effective[strings.ToLower(text[:space])] = NormaliseSshdValue(strings.TrimSpace(text[space+1:]))
	}
	return effective
}

// NormaliseSshdValue maps without-password, which sshd prints, to prohibit-password, which people
// write; every other value is left as sshd said it.
func NormaliseSshdValue(value string) string {
	if value == "without-password" {
		return "prohibit-password"
	}
	return value
}

// HasInclude reports whether sshd_config has an Include of the drop-in directory.
func HasInclude(text, directory string) bool {
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 || strings.ToLower(words[0]) != "include" {
			continue
		}
		second := ""
		if len(words) > 1 {
			second = words[1]
		}
		if strings.HasPrefix(second, directory) {
			return true
		}
	}
	return false
}

// Criterion is one Match criterion, such as Address 10.0.0.0/24, rendered in list order. In
// production the criteria reached the TypeScript sorted by key (see Setting); #45 passes them so.
type Criterion struct {
	Key   string
	Value string
}

// SshdDropIn renders the drop-in file, with its settings indented inside a Match block when there
// are criteria.
func SshdDropIn(settings []Keyword, match []Criterion) string {
	lines := make([]string, len(settings))
	for i, setting := range settings {
		lines[i] = setting.Keyword + " " + setting.Value
	}
	head := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`."
	if len(match) == 0 {
		return head + "\n" + strings.Join(lines, "\n") + "\n"
	}
	scope := make([]string, len(match))
	for i, criterion := range match {
		scope[i] = criterion.Key + " " + criterion.Value
	}
	indented := make([]string, len(lines))
	for i, line := range lines {
		indented[i] = "    " + line
	}
	return head + "\nMatch " + strings.Join(scope, " ") + "\n" + strings.Join(indented, "\n") + "\n"
}
