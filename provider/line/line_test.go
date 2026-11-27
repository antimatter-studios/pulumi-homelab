package line

import (
	"reflect"
	"strings"
	"testing"
)

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

var shims = Args{
	Line:     `export PATH="$HOME/.local/share/mise/shims:$PATH"`,
	Marker:   "mise-shims",
	Position: Before,
	Anchor:   "case $- in",
	Comment:  "#",
}


func with(args Args, change func(*Args)) Args {
	change(&args)
	return args
}

func countMarked(text string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "mise-shims") {
			n++
		}
	}
	return n
}

func eq[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestMarkLine(t *testing.T) {
	t.Run("writes the marker into the line as a trailing comment", func(t *testing.T) {
		eq(t, "MarkLine", MarkLine("export PATH=x", "mise-shims", DefaultComment), "export PATH=x # pulumi-homelab:mise-shims")
	})
	t.Run("takes the comment syntax of the file it is going into", func(t *testing.T) {
		eq(t, "MarkLine", MarkLine("x=1", "thing", ";"), "x=1 ; pulumi-homelab:thing")
	})
	t.Run("finds its own line and nothing else", func(t *testing.T) {
		text := "a\n" + MarkLine("export PATH=x", "mise-shims", DefaultComment) + "\nb"
		eq(t, "FindMarked", FindMarked(text, "mise-shims"), 1)
		eq(t, "FindMarked other", FindMarked(text, "something-else"), -1)
	})
	t.Run("is what makes a hand-edited line the same line rather than a second one", func(t *testing.T) {
		edited := "a\nexport PATH=\"/somewhere/else\" # pulumi-homelab:mise-shims\nb"
		eq(t, "FindMarked", FindMarked(edited, "mise-shims"), 1)
	})
}

func TestFindAnchor(t *testing.T) {
	t.Run("takes the first occurrence for before", func(t *testing.T) {
		eq(t, "FindAnchor", FindAnchor("x\ncase $- in\ny\ncase $- in", "case $- in", Before), 1)
	})
	t.Run("takes the last for after, so after a block means after all of it", func(t *testing.T) {
		eq(t, "FindAnchor", FindAnchor("x\ncase $- in\ny\ncase $- in", "case $- in", After), 3)
	})
	t.Run("answers nothing when the file does not contain it", func(t *testing.T) {
		eq(t, "FindAnchor", FindAnchor(guard, "no such text", Before), -1)
		eq(t, "FindAnchor empty", FindAnchor(guard, "", Before), -1)
	})
}

func TestInsertionPoint(t *testing.T) {
	t.Run("goes above the anchor for before", func(t *testing.T) {
		eq(t, "InsertionPoint", InsertionPoint(guard, shims), 3)
	})
	t.Run("goes below it for after", func(t *testing.T) {
		eq(t, "InsertionPoint", InsertionPoint(guard, with(shims, func(a *Args) { a.Position, a.Anchor = After, "esac" })), 7)
	})
	t.Run("goes at the top for prepend and the end for append", func(t *testing.T) {
		eq(t, "prepend", InsertionPoint(guard, Args{Position: Prepend}), 0)
		eq(t, "append", InsertionPoint(guard, Args{Position: Append}), len(strings.Split(guard, "\n")))
	})
}

func TestUpsertLine(t *testing.T) {
	t.Run("places it before the guard, where it actually runs", func(t *testing.T) {
		lines := strings.Split(UpsertLine(guard, shims), "\n")
		at, g := -1, -1
		for i, l := range lines {
			if at < 0 && strings.Contains(l, "mise-shims") {
				at = i
			}
			if g < 0 && strings.Contains(l, "case $- in") {
				g = i
			}
		}
		if !(at < g) {
			t.Errorf("line at %d, guard at %d", at, g)
		}
	})
	t.Run("leaves every other byte alone, comments and all", func(t *testing.T) {
		before := strings.Split(guard, "\n")
		var after []string
		for _, l := range strings.Split(UpsertLine(guard, shims), "\n") {
			if !strings.Contains(l, "mise-shims") {
				after = append(after, l)
			}
		}
		if !reflect.DeepEqual(after, before) {
			t.Errorf("got %q, want %q", after, before)
		}
	})
	t.Run("does not grow the file on a second application", func(t *testing.T) {
		once := UpsertLine(guard, shims)
		eq(t, "twice", UpsertLine(once, shims), once)
		eq(t, "copies", countMarked(once), 1)
	})
	t.Run("corrects the text of a line somebody edited, in place", func(t *testing.T) {
		edited := strings.Replace(UpsertLine(guard, shims), shims.Line, `export PATH="/wrong"`, 1)
		fixed := UpsertLine(edited, shims)
		eq(t, "fixed", fixed, UpsertLine(guard, shims))
		eq(t, "copies", countMarked(fixed), 1)
	})
	t.Run("moves a line that has crossed its anchor back, and takes the old copy with it", func(t *testing.T) {
		moved := guard + "\n" + MarkLine(shims.Line, shims.Marker, DefaultComment)
		eq(t, "placed before", CorrectlyPlaced(moved, shims), false)
		fixed := UpsertLine(moved, shims)
		eq(t, "placed after", CorrectlyPlaced(fixed, shims), true)
		eq(t, "copies", countMarked(fixed), 1)
	})
	t.Run("leaves a line alone that is above its anchor but not adjacent to it", func(t *testing.T) {
		lines := strings.Split(guard, "\n")
		g := -1
		for i, l := range lines {
			if strings.Contains(l, "case $- in") {
				g = i
				break
			}
		}
		placedHigh := strings.Join(append(append([]string{MarkLine(shims.Line, shims.Marker, shims.Comment)}, lines[:g]...), lines[g:]...), "\n")
		eq(t, "placed", CorrectlyPlaced(placedHigh, shims), true)
		eq(t, "FindMarked", FindMarked(placedHigh, shims.Marker), 0)
		eq(t, "upsert", UpsertLine(placedHigh, shims), placedHigh)
		eq(t, "FindMarked after upsert", FindMarked(UpsertLine(placedHigh, shims), shims.Marker), 0)
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		eq(t, "upsert", UpsertLine(guard, shims), UpsertLine(guard, shims))
	})
}

func TestReadLine(t *testing.T) {
	t.Run("reports it absent, and absent is not misplaced", func(t *testing.T) {
		eq(t, "ReadLine", ReadLine(guard, shims), Read{Actual: "", Placed: false})
	})
	t.Run("reports the line as the file has it, marker and all", func(t *testing.T) {
		eq(t, "Actual", ReadLine(UpsertLine(guard, shims), shims).Actual, MarkLine(shims.Line, shims.Marker, shims.Comment))
	})
	t.Run("reports a line that is present and inert", func(t *testing.T) {
		moved := guard + "\n" + MarkLine(shims.Line, shims.Marker, DefaultComment)
		eq(t, "ReadLine", ReadLine(moved, shims), Read{Actual: MarkLine(shims.Line, shims.Marker, DefaultComment), Placed: false})
	})
	t.Run("reports a correctly placed line as placed", func(t *testing.T) {
		eq(t, "Placed", ReadLine(UpsertLine(guard, shims), shims).Placed, true)
	})
	t.Run("calls an anchorless position placed wherever the line is", func(t *testing.T) {
		appending := with(shims, func(a *Args) { a.Position, a.Anchor = Append, "" })
		eq(t, "Placed", ReadLine(UpsertLine(guard, appending), appending).Placed, true)
	})
	t.Run("reports not-placed when the anchor itself has gone", func(t *testing.T) {
		withLine := UpsertLine(guard, shims)
		eq(t, "placed", CorrectlyPlaced(strings.Replace(withLine, "case $- in", `case "$-" in`, 1), shims), false)
	})
}

func TestRemoveLine(t *testing.T) {
	t.Run("removes its own line and leaves the rest", func(t *testing.T) {
		eq(t, "RemoveLine", RemoveLine(UpsertLine(guard, shims), shims.Marker), guard)
	})
	t.Run("leaves a file it was never in exactly as it found it", func(t *testing.T) {
		eq(t, "RemoveLine", RemoveLine(guard, shims.Marker), guard)
	})
	t.Run("does not remove another marked line", func(t *testing.T) {
		both := UpsertLine(UpsertLine(guard, shims), with(shims, func(a *Args) { a.Marker, a.Line = "other", "x=1" }))
		eq(t, "RemoveLine", RemoveLine(both, "other"), UpsertLine(guard, shims))
	})
}
