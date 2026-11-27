package mode

import (
	"strings"
	"testing"
)

func ptr(b bool) *bool { return &b }

func TestNormaliseMode(t *testing.T) {
	check := func(t *testing.T, in, want string) {
		t.Helper()
		if got := NormaliseMode(in); got != want {
			t.Errorf("NormaliseMode(%q) = %q, want %q", in, got, want)
		}
	}
	t.Run("gives stat’s three digits the leading zero everybody writes", func(t *testing.T) {
		check(t, "644", "0644")
		check(t, "755", "0755")
	})
	t.Run("leaves a mode that already has four alone", func(t *testing.T) {
		check(t, "0644", "0644")
	})
	t.Run("does not mistake a setgid bit for a leading zero", func(t *testing.T) {
		check(t, "2775", "2775")
		check(t, "4755", "4755")
		check(t, "1777", "1777")
	})
	t.Run("ignores the newline stat prints after it", func(t *testing.T) {
		check(t, "644\n", "0644")
	})
}

func TestSpecialBits(t *testing.T) {
	bits := func(t *testing.T, in string, want SpecialBits) {
		t.Helper()
		if got := SpecialBitsOf(in); got != want {
			t.Errorf("SpecialBitsOf(%q) = %+v, want %+v", in, got, want)
		}
	}
	with := func(t *testing.T, in string, flags Flags, want string) {
		t.Helper()
		if got := WithSpecialBits(in, flags); got != want {
			t.Errorf("WithSpecialBits(%q, %+v) = %q, want %q", in, flags, got, want)
		}
	}
	t.Run("reads what the leading digit says", func(t *testing.T) {
		bits(t, "2775", SpecialBits{Setgid: true})
		bits(t, "1777", SpecialBits{Sticky: true})
		bits(t, "4755", SpecialBits{Setuid: true})
		bits(t, "3775", SpecialBits{Setgid: true, Sticky: true})
	})
	t.Run("reads a three-digit mode as having none, which is what stat means by one", func(t *testing.T) {
		bits(t, "755", SpecialBits{})
		bits(t, "0755", SpecialBits{})
	})
	t.Run("folds a flag into the digit", func(t *testing.T) {
		with(t, "0775", Flags{Setgid: ptr(true)}, "2775")
		with(t, "1777", Flags{}, "1777")
		with(t, "775", Flags{Sticky: ptr(true), Setgid: ptr(true)}, "3775")
	})
	t.Run("always answers with four digits, so the comparison is against what stat reports", func(t *testing.T) {
		with(t, "755", Flags{}, "0755")
		with(t, "0755", Flags{}, "0755")
	})
	t.Run("takes a flag set to false as taking the bit off", func(t *testing.T) {
		with(t, "0775", Flags{Setgid: ptr(false)}, "0775")
		with(t, "775", Flags{Sticky: ptr(false)}, "0775")
	})
	t.Run("leaves a bit alone when nothing was said about it", func(t *testing.T) {
		with(t, "2775", Flags{Sticky: nil}, "2775")
		with(t, "2775", Flags{Sticky: ptr(true)}, "3775")
	})
	t.Run("does not touch the permission digits", func(t *testing.T) {
		with(t, "0640", Flags{Setgid: ptr(true)}, "2640")
	})
}

func TestSpecialBitsRefusal(t *testing.T) {
	t.Run("is quiet when only the mode says anything", func(t *testing.T) {
		if r := SpecialBitsRefusal("2775", Flags{}); r != "" {
			t.Errorf("got %q", r)
		}
		if r := SpecialBitsRefusal("2775", Flags{Sticky: nil}); r != "" {
			t.Errorf("got %q", r)
		}
	})
	t.Run("is quiet when only the flags do", func(t *testing.T) {
		if r := SpecialBitsRefusal("0755", Flags{Sticky: ptr(true)}); r != "" {
			t.Errorf("got %q", r)
		}
		if r := SpecialBitsRefusal("755", Flags{Setgid: ptr(true)}); r != "" {
			t.Errorf("got %q", r)
		}
	})
	t.Run("refuses a non-zero leading digit alongside a flag, rather than inventing a precedence", func(t *testing.T) {
		r := SpecialBitsRefusal("2775", Flags{Sticky: ptr(true)})
		if !strings.Contains(r, strings.Split("setgid or sticky", " or ")[1]) {
			t.Errorf("refusal %q does not name sticky", r)
		}
		if !strings.Contains(r, "'0775'") {
			t.Errorf("refusal %q does not carry the way out", r)
		}
	})
	t.Run("refuses a flag that outright contradicts the digit", func(t *testing.T) {
		if SpecialBitsRefusal("2775", Flags{Setgid: ptr(false)}) == "" {
			t.Error("not refused")
		}
	})
	t.Run("refuses a false flag beside a digit even when the two agree", func(t *testing.T) {
		if SpecialBitsRefusal("2775", Flags{Sticky: ptr(false)}) == "" {
			t.Error("not refused")
		}
	})
	t.Run("names every flag that was given", func(t *testing.T) {
		r := SpecialBitsRefusal("1775", Flags{Setgid: ptr(true), Setuid: ptr(true)})
		for _, want := range []string{"setuid", "setgid"} {
			if !strings.Contains(r, want) {
				t.Errorf("refusal %q does not name %s", r, want)
			}
		}
	})
}

func TestModeRefusal(t *testing.T) {
	t.Run("accepts three and four octal digits", func(t *testing.T) {
		for _, m := range []string{"755", "0755", "2775"} {
			if r := ModeRefusal(m); r != "" {
				t.Errorf("ModeRefusal(%q) = %q", m, r)
			}
		}
	})
	t.Run("refuses a digit that is not octal", func(t *testing.T) {
		if r := ModeRefusal("798"); !strings.Contains(r, "octal") {
			t.Errorf("got %q", r)
		}
	})
	t.Run("refuses a symbolic mode, which chmod takes and stat never reports", func(t *testing.T) {
		if ModeRefusal("u+rwx") == "" {
			t.Error("not refused")
		}
	})
	t.Run("refuses the wrong number of digits", func(t *testing.T) {
		for _, m := range []string{"75", "07555", ""} {
			if ModeRefusal(m) == "" {
				t.Errorf("ModeRefusal(%q) not refused", m)
			}
		}
	})
}
