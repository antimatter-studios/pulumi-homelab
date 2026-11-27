package archive

import "testing"

func TestFormatOf(t *testing.T) {
	check := func(t *testing.T, url string, want Format) {
		t.Helper()
		if got := FormatOf(url); got != want {
			t.Errorf("FormatOf(%q) = %q, want %q", url, got, want)
		}
	}
	t.Run("knows the four it can unpack, under both spellings", func(t *testing.T) {
		check(t, "https://x/a.tar.gz", TarGz)
		check(t, "https://x/a.tgz", TarGz)
		check(t, "https://x/a.tar.xz", TarXz)
		check(t, "https://x/a.txz", TarXz)
		check(t, "https://x/a.tar.bz2", TarBz2)
		check(t, "https://x/a.tbz", TarBz2)
		check(t, "https://x/a.zip", Zip)
	})
	t.Run("looks past the query string, which release hosts fill with signatures", func(t *testing.T) {
		check(t, "https://x/a.tar.gz?X-Amz-Expires=300&X-Amz-Signature=deadbeef", TarGz)
	})
	t.Run("says so rather than guessing when the name carries no extension", func(t *testing.T) {
		check(t, "https://api.example.com/artifacts/12345/download", "")
	})
	t.Run("does not mistake a version that contains one of the extensions", func(t *testing.T) {
		check(t, "https://x/widget-1.0.zip.sha256", "")
	})
}

func TestParseVersion(t *testing.T) {
	check := func(t *testing.T, out, pattern, want string) {
		t.Helper()
		if got := ParseVersion(out, pattern); got != want {
			t.Errorf("ParseVersion(%q, %q) = %q, want %q", out, pattern, got, want)
		}
	}
	t.Run("finds the usual shape without being told", func(t *testing.T) {
		check(t, "widget version 1.4.2", DefaultVersionPattern, "1.4.2")
		check(t, "1.4", DefaultVersionPattern, "1.4")
		check(t, "widget 1.4.2-rc.1+build7", DefaultVersionPattern, "1.4.2-rc.1+build7")
	})
	t.Run("takes a pattern for the programs the default gets wrong", func(t *testing.T) {
		check(t, "widget (c) 2024.1 Acme\nwidget/9.9.9", "widget/([0-9.]+)", "9.9.9")
	})
	t.Run("answers nothing rather than throwing when the program said nothing useful", func(t *testing.T) {
		check(t, "command not found", DefaultVersionPattern, "")
	})
	t.Run("answers nothing rather than throwing when the pattern does not compile", func(t *testing.T) {
		check(t, "widget 1.4.2", "([0-9", "")
	})
}

// The pattern is JavaScript's, and so are its \s and .: the default stops at a non-breaking space,
// a vertical tab or U+3000 as it does in Node, and . does not run into a \r.
func TestParseVersionMatchesJavaScriptSemantics(t *testing.T) {
	for _, tc := range []struct{ out, pattern, want string }{
		{"v 1.4.2\u00a0(abc)", DefaultVersionPattern, "1.4.2"},
		{"v 1.4.2\v(abc)", DefaultVersionPattern, "1.4.2"},
		{"v 1.4.2\u3000(abc)", DefaultVersionPattern, "1.4.2"},
		{"v 1.4.2\ufeff(abc)", DefaultVersionPattern, "1.4.2"},
		{"version 1.2\r\n", "version (.*)", "1.2"},
		{"version 1.2\u2028x", "version (.*)", "1.2"},
		{"version 1.2 x", "version (\\S+)", "1.2"},
		{"version\u00a01.2", "version\\s(\\S+)", "1.2"},
		{"a.b 7", "a\\.b ([0-9])", "7"},
		{"widget 1.4.2", "", ""}, // an explicit empty pattern is used as given, as new RegExp('') is
	} {
		if got := ParseVersion(tc.out, tc.pattern); got != tc.want {
			t.Errorf("ParseVersion(%q, %q) = %q, want %q", tc.out, tc.pattern, got, tc.want)
		}
	}
}
