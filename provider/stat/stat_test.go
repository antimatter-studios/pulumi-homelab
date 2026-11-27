package stat

import (
	"reflect"
	"testing"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseStat(t *testing.T) {
	t.Run("pads the mode into the shape the code writes it in", func(t *testing.T) {
		eq(t, ParseStat("directory|755|root|root").Mode, "0755")
	})
	t.Run("leaves a mode that is already four digits alone", func(t *testing.T) {
		eq(t, ParseStat("directory|0700|admin|admin").Mode, "0700")
	})
	t.Run("keeps a setgid mode, which is four digits for a different reason", func(t *testing.T) {
		eq(t, ParseStat("directory|2775|root|staff").Mode, "2775")
	})
	t.Run("reports the kind, so a file where a directory belongs can be told from an empty path", func(t *testing.T) {
		eq(t, ParseStat("regular file|644|root|root").Kind, "regular file")
		eq(t, ParseStat("symbolic link|777|root|root").Kind, "symbolic link")
	})
	t.Run("reads the owner and group", func(t *testing.T) {
		found := ParseStat("directory|0750|aiworld|aiworld")
		eq(t, []string{found.Owner, found.Group}, []string{"aiworld", "aiworld"})
	})
}

func TestParseFileStat(t *testing.T) {
	t.Run("reads the mode, the owner and the group", func(t *testing.T) {
		eq(t, ParseFileStat("644 root root"), FileStat{Mode: "0644", Owner: "root", Group: "root"})
	})
	t.Run("pads the mode into the shape the code writes it in", func(t *testing.T) {
		eq(t, ParseFileStat("600 admin admin").Mode, "0600")
	})
	t.Run("leaves a mode that already carries four digits alone", func(t *testing.T) {
		eq(t, ParseFileStat("2775 root staff").Mode, "2775")
	})
	t.Run("reads an owner whose name is not a word stat would pad differently", func(t *testing.T) {
		eq(t, ParseFileStat("640 systemd-network systemd-network"), FileStat{Mode: "0640", Owner: "systemd-network", Group: "systemd-network"})
	})
	t.Run("answers with empty strings rather than throwing on nothing", func(t *testing.T) {
		eq(t, ParseFileStat(""), FileStat{Mode: "", Owner: "", Group: ""})
	})
	t.Run("splits on JavaScript's whitespace, so a NEL is part of a name", func(t *testing.T) {
		eq(t, ParseFileStat("644 root\u0085root grp"), FileStat{Mode: "0644", Owner: "root\u0085root", Group: "grp"})
		eq(t, ParseFileStat("\ufeff644\u00a0root\u3000grp\n"), FileStat{Mode: "0644", Owner: "root", Group: "grp"})
	})
}

func TestParseStatTrimsAsJavaScriptDoes(t *testing.T) {
	eq(t, ParseStat("directory|755|root|root\u0085").Group, "root\u0085")
	eq(t, ParseStat("\ufeffdirectory|755|root|root\ufeff"), Stat{Kind: "directory", Mode: "0755", Owner: "root", Group: "root"})
}
