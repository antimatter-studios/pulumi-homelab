package systemd

import (
	"reflect"
	"testing"
)

type m = map[string]string

func TestParseShow(t *testing.T) {
	check := func(t *testing.T, got, want m) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}
	t.Run("reads values by key, whatever order systemd returns them in", func(t *testing.T) {
		check(t, ParseShow("ActiveState=active\nUnitFileState=enabled"), m{"ActiveState": "active", "UnitFileState": "enabled"})
	})
	t.Run("gives the same answer when the order is reversed", func(t *testing.T) {
		check(t, ParseShow("ActiveState=active\nUnitFileState=enabled"), ParseShow("UnitFileState=enabled\nActiveState=active"))
	})
	t.Run("keeps an empty value attached to its own key", func(t *testing.T) {
		check(t, ParseShow("UnitFileState=\nActiveState=active"), m{"UnitFileState": "", "ActiveState": "active"})
	})
	t.Run("omits a property systemd does not recognise rather than shifting the rest", func(t *testing.T) {
		if got, ok := ParseShow("ActiveState=active")["UnitFileState"]; ok {
			t.Errorf("got %q, want undefined", got)
		}
	})
	t.Run("ignores anything that is not a key=value line", func(t *testing.T) {
		check(t, ParseShow("some prose\n\nActiveState=active"), m{"ActiveState": "active"})
	})
	t.Run("keeps an = that appears inside a value", func(t *testing.T) {
		if got := ParseShow("ExecStart=/usr/bin/node --env=x server.mjs")["ExecStart"]; got != "/usr/bin/node --env=x server.mjs" {
			t.Errorf("got %q", got)
		}
	})
}

func TestEscapeInstance(t *testing.T) {
	check := func(t *testing.T, in, want string) {
		t.Helper()
		if got := EscapeInstance(in); got != want {
			t.Errorf("EscapeInstance(%q) = %q, want %q", in, got, want)
		}
	}
	t.Run("leaves a hostname exactly as it is, which is the common case", func(t *testing.T) {
		check(t, "photos.example.local", "photos.example.local")
	})
	t.Run("turns a path separator into a dash, as systemd does", func(t *testing.T) {
		check(t, "mnt/data", "mnt-data")
	})
	t.Run("escapes a leading dot, which would otherwise be a hidden file", func(t *testing.T) {
		check(t, ".hidden", `\x2ehidden`)
	})
	t.Run("escapes anything else as its bytes", func(t *testing.T) {
		check(t, "a b", `a\x20b`)
		check(t, "a@b", `a\x40b`)
	})
	t.Run("escapes a multi-byte character as every one of its bytes", func(t *testing.T) {
		check(t, "é", `\xc3\xa9`)
	})
	t.Run("keeps the characters systemd allows through", func(t *testing.T) {
		check(t, "a:b_c.d", "a:b_c.d")
	})
	t.Run("leaves an empty instance empty rather than inventing something", func(t *testing.T) {
		check(t, "", "")
	})
}

// JavaScript's trim keeps U+0085 (NEL) where Go's unicode.IsSpace would strip it, and strips U+FEFF
// where Go would keep it; ParseShow trims as JavaScript does.
func TestParseShowTrimsAsJavaScriptDoes(t *testing.T) {
	if got := ParseShow("ActiveState=active\u0085")["ActiveState"]; got != "active\u0085" {
		t.Fatalf("NEL: got %q", got)
	}
	if got := ParseShow("ActiveState=active\ufeff")["ActiveState"]; got != "active" {
		t.Fatalf("BOM: got %q", got)
	}
}
