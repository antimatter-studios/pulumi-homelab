package apt

import "testing"

func TestParseDpkgStatus(t *testing.T) {
	installed := func(t *testing.T, in, want string) {
		t.Helper()
		got, ok := ParseDpkgStatus(in)
		if !ok || got != want {
			t.Errorf("ParseDpkgStatus(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	absent := func(t *testing.T, in string) {
		t.Helper()
		if got, ok := ParseDpkgStatus(in); ok {
			t.Errorf("ParseDpkgStatus(%q) = %q, true; want null", in, got)
		}
	}
	t.Run("gives the version of a package that is installed", func(t *testing.T) {
		installed(t, "installed 20.11.1-1", "20.11.1-1")
	})
	t.Run("reports nothing for a package dpkg remembers and has removed", func(t *testing.T) {
		absent(t, "deinstall 2:20.2+dfsg-1")
		absent(t, "config-files 1.0-1")
	})
	t.Run("reports nothing for a package dpkg has never heard of", func(t *testing.T) {
		absent(t, "")
	})
	t.Run("gives an empty version rather than null when dpkg answers without one", func(t *testing.T) {
		installed(t, "installed", "")
	})
	t.Run("is unmoved by the whitespace dpkg pads its output with", func(t *testing.T) {
		installed(t, "  installed   1.0-1  \n", "1.0-1")
	})
}
