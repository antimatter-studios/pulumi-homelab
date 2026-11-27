package apt

// The pure parsing for the AptPackages resource, ported from src/resources/packages.ts.

import (
	"regexp"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/aptmark"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// ParseInstalled reads `dpkg-query -W -f='${binary:Package} ${db:Status-Status} ${Version}\n'`
// into the installed packages and their versions. A package dpkg knows but has removed is left
// out; a later line for the same name wins, as in the TypeScript object.
func ParseInstalled(out string) map[string]string {
	installed := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		words := append(jscompat.Fields(line), "", "", "")
		name, status, version := words[0], words[1], words[2]
		if name == "" {
			continue
		}
		if status == "installed" {
			installed[name] = version
		}
	}
	return installed
}

const manualMarker = "#pulumi-homelab#manual"

// PackageState is dpkg's answer and apt's marking for one set of names.
type PackageState struct {
	Installed map[string]string
	Manual    []string
}

// ParsePackageState splits the one-round-trip reply at the marker into dpkg's answer and the
// names apt holds as manual.
func ParsePackageState(out string) PackageState {
	parts := strings.Split(out, manualMarker+"\n")
	dpkg, marked := parts[0], ""
	if len(parts) > 1 {
		marked = parts[1]
	}
	return PackageState{Installed: ParseInstalled(dpkg), Manual: aptmark.ParseManual(marked)}
}

var removal = regexp.MustCompile(`^Remv` + jscompat.SpaceClass + `+(` + jscompat.NonSpaceClass + `+)`)

// ParseSimulatedRemovals reads the packages an `apt-get -s purge` would remove off its Remv lines.
func ParseSimulatedRemovals(out string) []string {
	names := []string{}
	for _, line := range strings.Split(out, "\n") {
		if found := removal.FindStringSubmatch(jscompat.Trim(line)); found != nil && found[1] != "" {
			names = append(names, found[1])
		}
	}
	return names
}
