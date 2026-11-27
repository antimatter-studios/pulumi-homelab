package homelab

import (
	"regexp"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/line"
)

// Ported from src/resources/line.test.ts: refusing to place a line where it would be inert, and
// settling the arguments.

var guard = strings.Join([]string{
	"# ~/.bashrc: executed by bash(1) for non-login shells.",
	"",
	"# If not running interactively, don't do anything",
	"case $- in",
	"    *i*) ;;",
	"      *) return;;",
	"esac",
	"",
	"HISTSIZE=1000",
	"alias ll='ls -alF'",
}, "\n")

func TestPlacementRefusal(t *testing.T) {
	for _, position := range []line.Position{line.Prepend, line.Append} {
		if got := placementRefusal(guard, "/x", position, ""); got != "" {
			t.Errorf("%s: %q", position, got)
		}
	}
	if got := placementRefusal(guard, "/home/chris/.bashrc", line.Before, "case $- in"); got != "" {
		t.Errorf("anchor present: %q", got)
	}
	if got := placementRefusal(guard, "/x", line.Before, ""); !regexp.MustCompile(`needs an anchor`).MatchString(got) {
		t.Errorf("no anchor: %q", got)
	}
	refusal := placementRefusal(guard, "/home/chris/.bashrc", line.Before, "case $x in")
	if !strings.Contains(refusal, "/home/chris/.bashrc") || !strings.Contains(refusal, "may never run") {
		t.Errorf("absent anchor: %q", refusal)
	}
	if !strings.Contains(refusal, `does not contain "case $x in", so`) {
		t.Errorf("the anchor is quoted as JSON.stringify does: %q", refusal)
	}
}

func TestResolveLine(t *testing.T) {
	settled, id, err := resolveLine(ManagedLineArgs{Path: "/etc/x", Line: "y", Marker: "z"})
	if err != nil || settled.Position != "append" || settled.Anchor != "" || settled.Comment != "#" {
		t.Errorf("defaults: %+v %v", settled, err)
	}
	if id != "/etc/x#z" {
		t.Errorf("id %q", id)
	}
	if _, _, err := resolveLine(ManagedLineArgs{Path: "/etc/x", Line: "y", Marker: "  "}); err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("empty marker: %v", err)
	}
}
