package archive

import (
	"strings"
	"testing"
)

// From the #36 review of #62, for the Archive port (#44): a versionPattern RE2 cannot compile, or
// would read differently from JavaScript, is refused, naming the field.
func TestPatternRefusal(t *testing.T) {
	t.Run("accepts the default and the patterns the TypeScript tests use", func(t *testing.T) {
		for _, p := range []string{DefaultVersionPattern, `widget/([0-9.]+)`, `v(\d+\.\d+)`, `(?:go)?(\S+)`,
			`(?<v>[0-9]+)`, `^\s*([\w.-]+)$`, `\x41(\d)`, `[^\s]+ ([0-9]+)`, `a\.b\/c\-(\d)`, `[]x(\d)`, `[^](\d)`} {
			if got := PatternRefusal(p); got != "" {
				t.Errorf("%q refused: %s", p, got)
			}
		}
	})
	t.Run("refuses what RE2 cannot compile, naming the field", func(t *testing.T) {
		for _, p := range []string{`(?=v)([0-9]+)`, `(?<!x)([0-9]+)`, `(a)\1`, `([0-9`, `\k<v>`} {
			got := PatternRefusal(p)
			if !strings.Contains(got, "versionPattern") || !strings.Contains(got, "does not compile as RE2") {
				t.Errorf("%q: %q", p, got)
			}
		}
	})

}

// An unflagged JavaScript RegExp reads text as UTF-16 code units; so does ParseVersion.
func TestParseVersionReadsUTF16AsJavaScriptDoes(t *testing.T) {
	cases := []struct{ out, pattern, want string }{
		{"😀42", `^.([0-9]+)$`, ""},
		{"a42", `^.([0-9]+)$`, "42"},
		{"😀42", `^..([0-9]+)$`, "42"},
		{"widget 1.4.2😀 x", DefaultVersionPattern, "1.4.2😀"},
		{"v😀", `v(.)`, "\ufffd"},
		{"v😀", `v([^a]+)`, "😀"},
		{"é 2.0", `([0-9.]+)`, "2.0"},
	}
	for _, c := range cases {
		if got := ParseVersion(c.out, c.pattern); got != c.want {
			t.Errorf("ParseVersion(%q, %q) = %q, want %q", c.out, c.pattern, got, c.want)
		}
	}
}
