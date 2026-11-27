package homelab

import (
	"regexp"
	"strings"
	"testing"
)

var debArgs = DebPackageArgs{
	Name:   "widget-cli",
	URL:    "https://github.com/example/widget-cli/releases/download/v0.1.0/widget-cli_0.1.0_arm64.deb",
	Sha256: strings.Repeat("a", 64),
}

func debScript() string { return debInstallScript(debArgs.URL, debArgs.Sha256) }

// Ported from src/resources/deb.test.ts: composing the install.
func TestDebInstallScript(t *testing.T) {
	t.Run("verifies before it installs", func(t *testing.T) {
		s := debScript()
		if strings.Index(s, "sha256sum -c") >= strings.Index(s, aptGet+" install") {
			t.Fatal(s)
		}
	})
	t.Run("stops the whole command when verification fails", func(t *testing.T) {
		if !strings.HasPrefix(debScript(), "set -e") {
			t.Fatal(debScript())
		}
	})
	t.Run("downloads into a private directory rather than a predictable path", func(t *testing.T) {
		s := debScript()
		if !strings.Contains(s, "mktemp -d") || regexp.MustCompile(`-o ['"]?/tmp/[a-z]`).MatchString(s) {
			t.Fatal(s)
		}
	})
	t.Run("removes the file whether or not it was accepted", func(t *testing.T) {
		if !strings.Contains(debScript(), "trap") {
			t.Fatal(debScript())
		}
	})
	t.Run("installs through apt rather than dpkg, so dependencies resolve", func(t *testing.T) {
		s := debScript()
		if !strings.Contains(s, aptGet+" install") || strings.Contains(s, "dpkg -i") {
			t.Fatal(s)
		}
	})
	t.Run("never prompts", func(t *testing.T) {
		if !strings.Contains(debScript(), "DEBIAN_FRONTEND=noninteractive") {
			t.Fatal(debScript())
		}
	})
	t.Run("quotes the url, so a query string cannot become shell syntax", func(t *testing.T) {
		s := debInstallScript("https://example.com/p.deb?a=1&b=2", debArgs.Sha256)
		if !strings.Contains(s, "'https://example.com/p.deb?a=1&b=2'") || regexp.MustCompile(`curl [^']*&b=2`).MatchString(s) {
			t.Fatal(s)
		}
	})
	t.Run("quotes the checksum", func(t *testing.T) {
		if !strings.Contains(debScript(), "'"+strings.Repeat("a", 64)+"'") {
			t.Fatal(debScript())
		}
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		if debScript() != debScript() {
			t.Fatal("differs")
		}
	})
}

// Ported from src/resources/deb.test.ts: what an update actually does.
func TestDebUpdate(t *testing.T) {
	t.Run("downloads into a private directory rather than a predictable path", func(t *testing.T) {
		s := debScript()
		if !strings.Contains(s, "mktemp -d") || strings.Contains(s, "/tmp/pkg.deb") {
			t.Fatal(s)
		}
	})
	t.Run("cleans up even when verification fails", func(t *testing.T) {
		if !strings.Contains(debScript(), "trap") {
			t.Fatal(debScript())
		}
	})
	t.Run("uses apt rather than dpkg, so dependencies are resolved", func(t *testing.T) {
		s := debScript()
		if !strings.Contains(s, aptGet+" install") || strings.Contains(s, "dpkg -i") {
			t.Fatal(s)
		}
	})
}

// The whole script, since its text is what runs.
func TestDebInstallScriptExact(t *testing.T) {
	want := `set -e; dir=$(mktemp -d); trap "rm -rf \"$dir\"" EXIT; curl -fsSL --retry 3 -o "$dir/pkg.deb" 'https://x/p.deb'; ` +
		`echo 'abc'"  $dir/pkg.deb" | sha256sum -c - >/dev/null; DEBIAN_FRONTEND=noninteractive ` + aptGet + ` install -y -qq "$dir/pkg.deb"`
	if got := debInstallScript("https://x/p.deb", "abc"); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
