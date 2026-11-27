// Package cmdline holds the pure logic from src/resources/cmdline.ts: merging parameters into
// the kernel command line and composing the check against /proc/cmdline.
package cmdline

import (
	"regexp"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

func keyOf(parameter string) string { return strings.SplitN(parameter, "=", 2)[0] }

// Merge sets the parameters asked for and changes nothing else: a parameter already on the line
// (by key) is replaced in place, the rest are appended in the order asked for, and runs of
// whitespace (JavaScript's \s, so a BOM counts and a NEL does not) collapse to one space. Where two flags share a key, the later one wins.
func Merge(current string, flags []string) string {
	wanted := map[string]string{}
	for _, flag := range flags {
		wanted[keyOf(flag)] = flag
	}
	replaced := map[string]bool{}
	out := []string{}
	for _, parameter := range jscompat.Fields(current) {
		key := keyOf(parameter)
		if wants, ok := wanted[key]; ok {
			replaced[key] = true
			out = append(out, wants)
			continue
		}
		out = append(out, parameter)
	}
	for _, flag := range flags {
		if !replaced[keyOf(flag)] {
			out = append(out, flag)
		}
	}
	return strings.Join(out, " ")
}

var special = regexp.MustCompile(`[.\[\]{}()*+?^$|\\]`)

func escaped(text string) string { return special.ReplaceAllString(text, `\$0`) }

// BootedWith is a shell check that the running kernel was booted with every one of the flags,
// each matched as literal text anchored on both sides.
func BootedWith(flags []string) string {
	checks := make([]string, len(flags))
	for i, flag := range flags {
		checks[i] = `grep -qE '(^| )` + escaped(flag) + `( |$)' /proc/cmdline`
	}
	return strings.Join(checks, " && ")
}
