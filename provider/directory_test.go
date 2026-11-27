package homelab

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/stat"
)

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }

func mustResolve(t *testing.T, args DirectoryArgs) string {
	t.Helper()
	got, err := resolveMode(args)
	if err != nil {
		t.Fatalf("resolveMode(%+v): %v", args, err)
	}
	return got
}

// Ported from src/resources/directory.test.ts › settling a mode and the bits beside it.
func TestResolveMode(t *testing.T) {
	t.Run("defaults to 0755 when nothing is said", func(t *testing.T) {
		if got := mustResolve(t, DirectoryArgs{}); got != "0755" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("folds the flags into the mode, so one string is written, compared and read back", func(t *testing.T) {
		for _, c := range []struct {
			args DirectoryArgs
			want string
		}{
			{DirectoryArgs{Mode: strp("0775"), Setgid: boolp(true)}, "2775"},
			{DirectoryArgs{Mode: strp("0777"), Sticky: boolp(true)}, "1777"},
			{DirectoryArgs{Mode: strp("0775"), Setgid: boolp(true), Sticky: boolp(true)}, "3775"},
		} {
			if got := mustResolve(t, c.args); got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		}
	})
	t.Run("keeps a four-digit mode meaning what it always has", func(t *testing.T) {
		if got := mustResolve(t, DirectoryArgs{Mode: strp("2775")}); got != "2775" {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("refuses a mode and a flag that both answer the same question", func(t *testing.T) {
		_, err := resolveMode(DirectoryArgs{Mode: strp("2775"), Sticky: boolp(true)})
		if err == nil || !strings.Contains(err.Error(), "one or the other") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("refuses a mode that is not a mode, at the declaration rather than at the machine", func(t *testing.T) {
		for _, m := range []string{"u+rwx", "789"} {
			if _, err := resolveMode(DirectoryArgs{Mode: strp(m)}); err == nil {
				t.Errorf("%s was accepted", m)
			}
		}
	})
	t.Run("carries a setuid bit through a four-digit mode, which is how such a directory is adopted", func(t *testing.T) {
		got := mustResolve(t, DirectoryArgs{Mode: strp("4755")})
		if got != "4755" || got != stat.ParseStat("directory|4755|root|root").Mode {
			t.Fatalf("got %s", got)
		}
	})
	t.Run("leaves a setuid bit alone when a flag beside it sets another", func(t *testing.T) {
		_, err := resolveMode(DirectoryArgs{Mode: strp("4755"), Sticky: boolp(true)})
		if err == nil || !strings.Contains(err.Error(), "one or the other") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("is the same answer every time, so nothing here can look like a change", func(t *testing.T) {
		a := mustResolve(t, DirectoryArgs{Mode: strp("775"), Setgid: boolp(true)})
		b := mustResolve(t, DirectoryArgs{Mode: strp("775"), Setgid: boolp(true)})
		if a != b {
			t.Fatalf("%s != %s", a, b)
		}
	})
	t.Run("answers in the shape stat reports, for a mode with no special bits", func(t *testing.T) {
		if got, want := mustResolve(t, DirectoryArgs{Mode: strp("755")}), stat.ParseStat("directory|755|root|root").Mode; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
		if got, want := mustResolve(t, DirectoryArgs{Mode: strp("0775"), Setgid: boolp(true)}), stat.ParseStat("directory|2775|root|root").Mode; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}
