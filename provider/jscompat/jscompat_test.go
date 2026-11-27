package jscompat

import (
	"math"
	"regexp"
	"slices"
	"testing"
	"unicode"
)

// The answers below are node's (see TestAgainstNode); they pin the edges without needing node.

func TestIsSpaceIsUnicodeSpacePlusBOMMinusNEL(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		want := unicode.IsSpace(r)
		switch r {
		case 0xFEFF:
			want = true
		case 0x85:
			want = false
		}
		if IsSpace(r) != want {
			t.Errorf("IsSpace(%U) = %v", r, !want)
		}
	}
}

func TestSpaceClassesMatchIsSpace(t *testing.T) {
	space := regexp.MustCompile(`^` + SpaceClass + `$`)
	nonSpace := regexp.MustCompile(`^` + NonSpaceClass + `$`)
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		s := string(r)
		if space.MatchString(s) != IsSpace(r) || nonSpace.MatchString(s) == IsSpace(r) {
			t.Errorf("%U: SpaceClass %v, NonSpaceClass %v, IsSpace %v", r, space.MatchString(s), nonSpace.MatchString(s), IsSpace(r))
		}
	}
	remv := regexp.MustCompile(`^Remv` + SpaceClass + `+(` + NonSpaceClass + `+)`)
	if m := remv.FindStringSubmatch("Remv\v\u00a0foo\u3000bar"); m == nil || m[1] != "foo" {
		t.Errorf("/^Remv\\s+(\\S+)/ on VT, NBSP, U+3000: %q", m)
	}
}

func TestTrimAndFields(t *testing.T) {
	for _, c := range []struct{ in, trim, start, end string }{
		{"", "", "", ""},
		{" \t\n\v\f\r", "", "", ""},
		{"\ufeff[global]\n", "[global]", "[global]\n", "\ufeff[global]"},
		{"\u0085x\u0085", "\u0085x\u0085", "\u0085x\u0085", "\u0085x\u0085"},
		{"\u00a0a b\u3000", "a b", "a b\u3000", "\u00a0a b"},
	} {
		if Trim(c.in) != c.trim || TrimStart(c.in) != c.start || TrimEnd(c.in) != c.end {
			t.Errorf("%q: %q %q %q", c.in, Trim(c.in), TrimStart(c.in), TrimEnd(c.in))
		}
	}
	if got := Fields("\v a\u00a0b\u0085c\u2028 d "); !slices.Equal(got, []string{"a", "b\u0085c", "d"}) {
		t.Errorf("Fields = %q", got)
	}
	if got := Fields(" \u3000 "); len(got) != 0 {
		t.Errorf("Fields of blanks = %q", got)
	}
	for in, want := range map[string][]string{
		"":       {""},
		" a  b ": {"", "a", "b", ""},
		"a\vb":   {"a", "b"},
		"\ufeff": {"", ""},
	} {
		if got := Split(in); !slices.Equal(got, want) {
			t.Errorf("Split(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNumber(t *testing.T) {
	nan := math.NaN()
	for in, want := range map[string]float64{
		"": 0, " \n": 0, "\ufeff": 0, "42": 42, " 42\u3000": 42, "-0": math.Copysign(0, -1), "+1": 1,
		"1.": 1, ".5": 0.5, "1e3": 1000, "1E-3": 0.001, "007": 7, "0x10": 16, "0X1f": 31, "0o17": 15,
		"0b101": 5, "Infinity": math.Inf(1), "+Infinity": math.Inf(1), "-Infinity": math.Inf(-1),
		"1e400": math.Inf(1), "1e-400": 0, "0x" + "ffffffffffffffffffff": 1.2089258196146292e24,
		"\u00851": nan, ".": nan, "1e": nan, "e1": nan, "-0x10": nan, "0x": nan, "0x1p4": nan,
		"1_000": nan, "inf": nan, "infinity": nan, "NaN": nan, "1,000": nan, "1 000": nan, "١": nan,
		"0b2": nan, "0o8": nan, "--1": nan, "+-1": nan,
	} {
		got := Number(in)
		if math.IsNaN(want) != math.IsNaN(got) || (!math.IsNaN(want) && (got != want || math.Signbit(got) != math.Signbit(want))) {
			t.Errorf("Number(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	for _, c := range []struct {
		in   float64
		want string
	}{
		{0, "0"}, {math.Copysign(0, -1), "0"}, {math.NaN(), "NaN"}, {math.Inf(1), "Infinity"},
		{math.Inf(-1), "-Infinity"}, {1e6, "1000000"}, {-1e6, "-1000000"}, {1.5, "1.5"},
		{1e20, "100000000000000000000"}, {1e21, "1e+21"}, {1.5e21, "1.5e+21"}, {123e-20, "1.23e-18"},
		{1e-6, "0.000001"}, {1e-7, "1e-7"}, {1.5e-7, "1.5e-7"}, {0.30000000000000004, "0.30000000000000004"},
		{math.MaxFloat64, "1.7976931348623157e+308"}, {5e-324, "5e-324"},
		{9007199254740993, "9007199254740992"}, {123456789012345680000, "123456789012345680000"},
		{-123.456, "-123.456"}, {4.35, "4.35"},
	} {
		if got := FormatNumber(c.in); got != c.want {
			t.Errorf("FormatNumber(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRound(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.5, 1}, {1.5, 2}, {2.5, 3}, {-0.5, math.Copysign(0, -1)}, {-1.5, -1}, {-2.5, -2},
		{0.49999999999999994, 0}, {-0.49999999999999994, math.Copysign(0, -1)},
		{4503599627370497, 4503599627370497}, {-4503599627370497, -4503599627370497},
		{4503599627370495.5, 4503599627370496}, {9007199254740991, 9007199254740991},
		{math.Inf(1), math.Inf(1)}, {math.Copysign(0, -1), math.Copysign(0, -1)},
	} {
		got := Round(c.in)
		if got != c.want || math.Signbit(got) != math.Signbit(c.want) {
			t.Errorf("Round(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if !math.IsNaN(Round(math.NaN())) {
		t.Error("Round(NaN) is not NaN")
	}
}

func TestKeyOrder(t *testing.T) {
	got := KeyOrder([]string{"path", "10", "comment", "2", "01", "-1", "4294967295", "4294967294", "0", "1.0"})
	want := []string{"0", "2", "10", "4294967294", "path", "comment", "01", "-1", "4294967295", "1.0"}
	if !slices.Equal(got, want) {
		t.Errorf("KeyOrder = %q, want %q", got, want)
	}
}

func TestSortedKeys(t *testing.T) {
	m := map[string]int{"path": 1, "read only": 1, "Path": 1, "10": 1, "9": 1, "\uffff": 1, "\U0001F600": 1, "browseable": 1}
	want := []string{"9", "10", "Path", "browseable", "path", "read only", "\U0001F600", "\uffff"}
	if got := SortedKeys(m); !slices.Equal(got, want) {
		t.Errorf("SortedKeys = %q, want %q", got, want)
	}
}

func TestCase(t *testing.T) {
	for in, want := range map[string]string{"abcXYZ09": "ABCXYZ09", "straße": "STRASSE", "ﬁ": "FI", "ŉ": "ʼN", "é": "É"} {
		if got := ToUpper(in); got != want {
			t.Errorf("ToUpper(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"ABCxyz09": "abcxyz09", "ÉSS": "éss", "\u212a": "k"} {
		if got := ToLower(in); got != want {
			t.Errorf("ToLower(%q) = %q, want %q", in, got, want)
		}
	}
}
