package jscompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// TestAgainstNode is the differential test: it sends a few hundred thousand inputs to
// testdata/oracle.mjs and compares every answer with this package's. It needs node on PATH (or in
// $JSCOMPAT_NODE) and skips without it; the table tests in jscompat_test.go pin the answers it found.
func TestAgainstNode(t *testing.T) {
	node := os.Getenv("JSCOMPAT_NODE")
	if node == "" {
		var err error
		if node, err = exec.LookPath("node"); err != nil {
			if os.Getenv("JSCOMPAT_REQUIRE_NODE") != "" {
				t.Fatal("node not found, and JSCOMPAT_REQUIRE_NODE says this run must compare against it")
			}
			t.Skip("node not found; set JSCOMPAT_NODE to run the differential test")
		}
	}
	rng := rand.New(rand.NewPCG(36, 75))

	req := struct {
		Strings  []string   `json:"strings"`
		Floats   []string   `json:"floats"`
		KeyLists [][]string `json:"keyLists"`
	}{Strings: numberStrings(rng), Floats: floatBits(rng), KeyLists: keyLists(rng)}
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "testdata/oracle.mjs")
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var res struct {
		Unicode    string     `json:"unicode"`
		Unassigned [][2]rune  `json:"unassigned"`
		Spaces     []rune     `json:"spaces"`
		Cases      [][3]any   `json:"cases"`
		Trim       [][4]any   `json:"trim"`
		Number     []string   `json:"number"`
		Format     []string   `json:"format"`
		Round      []string   `json:"round"`
		KeyOrder   [][]string `json:"keyOrder"`
		SortedKeys [][]string `json:"sortedKeys"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatal(err)
	}

	t.Run("IsSpace", func(t *testing.T) {
		var got []rune
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if IsSpace(r) {
				got = append(got, r)
			}
		}
		if !slices.Equal(got, res.Spaces) {
			t.Errorf("IsSpace set %U, node %U", got, res.Spaces)
		}
	})

	t.Run("Trim", func(t *testing.T) {
		bad := 0
		for i, s := range req.Strings {
			want := res.Trim[i]
			split := want[3].([]any)
			var wantSplit []string
			for _, f := range split {
				wantSplit = append(wantSplit, f.(string))
			}
			wantFields := slices.DeleteFunc(slices.Clone(wantSplit), func(f string) bool { return f == "" })
			if Trim(s) != want[0] || TrimStart(s) != want[1] || TrimEnd(s) != want[2] ||
				!slices.Equal(Split(s), wantSplit) || !slices.Equal(nonNil(Fields(s)), nonNil(wantFields)) {
				if bad++; bad < 10 {
					t.Errorf("%q: trim %q/%q/%q split %q fields %q; node %q", s, Trim(s), TrimStart(s), TrimEnd(s), Split(s), Fields(s), want)
				}
			}
		}
	})

	t.Run("Number", func(t *testing.T) {
		bad := 0
		for i, s := range req.Strings {
			if got := bits(Number(s)); got != res.Number[i] {
				if bad++; bad < 10 {
					t.Errorf("Number(%q) = %s, node %s", s, got, res.Number[i])
				}
			}
		}
	})

	t.Run("FormatNumber", func(t *testing.T) {
		bad := 0
		for i, h := range req.Floats {
			if got := FormatNumber(fromBits(h)); got != res.Format[i] {
				if bad++; bad < 10 {
					t.Errorf("FormatNumber(%s) = %s, node %s", h, got, res.Format[i])
				}
			}
		}
	})

	t.Run("Round", func(t *testing.T) {
		bad := 0
		for i, h := range req.Floats {
			if got := bits(Round(fromBits(h))); got != res.Round[i] {
				if bad++; bad < 10 {
					t.Errorf("Round(%v) = %s, node %s", fromBits(h), got, res.Round[i])
				}
			}
		}
	})

	t.Run("KeyOrder", func(t *testing.T) {
		for i, keys := range req.KeyLists {
			if got := KeyOrder(keys); !slices.Equal(got, res.KeyOrder[i]) {
				t.Errorf("KeyOrder(%q) = %q, node %q", keys, got, res.KeyOrder[i])
			}
			m := map[string]int{}
			for _, k := range keys {
				m[k] = 1
			}
			if got := SortedKeys(m); !slices.Equal(got, res.SortedKeys[i]) {
				t.Errorf("SortedKeys(%q) = %q, node %q", keys, got, res.SortedKeys[i])
			}
		}
	})

	t.Run("Case", func(t *testing.T) {
		t.Logf("Unicode: Go %s, node %s", unicode.Version, res.Unicode)
		// A code point node's Unicode does not assign yet is a version difference, not a bug here:
		// Go 1.27 (Unicode 17) case-maps U+A7CE and others that node 23 (Unicode 16) leaves alone.
		nodeUnassigned := func(r rune) bool {
			for _, run := range res.Unassigned {
				if r >= run[0] && r <= run[1] {
					return true
				}
			}
			return false
		}
		// Code points Go's tables do not know yet (and the ones paired with them) are documented
		// divergences, as are İ and final sigma; everything else must match.
		known := func(s string) bool {
			for _, r := range s {
				if !unicode.IsGraphic(r) && !unicode.IsControl(r) {
					return false
				}
			}
			return true
		}
		changed := map[rune]bool{}
		for _, c := range res.Cases {
			r := rune(c[0].(float64))
			changed[r] = true
			upper, lower := c[1].(string), c[2].(string)
			s := string(r)
			if !known(s) || !known(upper) || !known(lower) || nodeUnassigned(r) {
				continue
			}
			if got := ToUpper(s); got != upper {
				t.Errorf("ToUpper(%U) = %q, node %q", r, got, upper)
			}
			if got := ToLower(s); got != lower && r != 0x130 {
				t.Errorf("ToLower(%U) = %q, node %q", r, got, lower)
			}
		}
		for r := rune(0); r <= unicode.MaxRune; r++ {
			if changed[r] || (r >= 0xD800 && r <= 0xDFFF) || nodeUnassigned(r) {
				continue
			}
			s := string(r)
			if ToUpper(s) != s || ToLower(s) != s {
				t.Errorf("%U: Go changes its case (%q, %q), node leaves it", r, ToUpper(s), ToLower(s))
			}
		}
	})
	t.Logf("compared with node (Unicode %s): %d strings, %d floats, %d key lists",
		res.Unicode, len(req.Strings), len(req.Floats), len(req.KeyLists))
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// bits is f's bit pattern, with every NaN written as JavaScript's one NaN: the payload is not
// observable from JavaScript, and Go's math.NaN() carries a different one.
func bits(f float64) string {
	if math.IsNaN(f) {
		return "7ff8000000000000"
	}
	return fmt.Sprintf("%016x", math.Float64bits(f))
}

func fromBits(h string) float64 {
	u, err := strconv.ParseUint(h, 16, 64)
	if err != nil {
		panic(err)
	}
	return math.Float64frombits(u)
}

// numberStrings is the hand-picked edge cases plus random strings over the alphabet Number's
// grammar is made of, with every whitespace character in play.
func numberStrings(rng *rand.Rand) []string {
	out := []string{
		"", " ", "\ufeff", "\u0085", "\u00851", "\ufeff1\u3000", "\v1\f", "0", "-0", "+0", "00", "007",
		"1", "+1", "-1", "1.", ".1", ".", "+.", "-.5", "1e3", "1E3", "1e+3", "1e-3", "1e", "e1", "1e3.5",
		"Infinity", "+Infinity", "-Infinity", "infinity", "inf", "Inf", "NaN", "nan", "INFINITY",
		"0x10", "0X1f", "0x", "-0x10", "+0x10", "0x1p3", "0o17", "0O17", "0o8", "0b101", "0B2", "0b",
		"1_000", "1,000", "1 000", "0.1.2", "--1", "+-1", "1e1000", "-1e1000", "1e-400", "-1e-400",
		"0x" + strings.Repeat("f", 40), "0b" + strings.Repeat("1", 70), "0o" + strings.Repeat("7", 30),
		"9007199254740993", "123456789012345678901234567890", "4.9406564584124654e-324",
		"2.4703282292062327e-324", "2.4703282292062328e-324", "1.7976931348623157e308",
		"1.7976931348623158e308", "1.7976931348623159e308", "0.000001", "1e21", "1e+21",
		"12abc", "abc", "١", "１", "1\u00a0", "\u20281\u2029", "\u180e1", "\u200b1",
		"a\u00a0b", " a  b\t\nc ", "\u3000\u3000", "\ufeff[global]",
	}
	const alphabet = "0123456789+-.eExXoObBInfinityaN_ \t\n\u00a0\ufeff\u0085"
	runes := []rune(alphabet)
	for range 20000 {
		n := rng.IntN(8)
		var b strings.Builder
		for range n {
			b.WriteRune(runes[rng.IntN(len(runes))])
		}
		out = append(out, b.String())
	}
	for range 5000 {
		f := math.Float64frombits(rng.Uint64())
		out = append(out, strconv.FormatFloat(f, 'g', -1, 64), strconv.FormatFloat(f, 'e', rng.IntN(20), 64))
	}
	return out
}

// floatBits is the boundaries of String()'s three layouts and Math.round's halves, plus random bit
// patterns, random integers and random short decimals.
func floatBits(rng *rand.Rand) []string {
	fs := []float64{
		0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), 1, -1, 0.5, -0.5, 1.5, -1.5,
		2.5, -2.5, 0.49999999999999994, -0.49999999999999994, 0.5000000000000001, -0.5000000000000001,
		4503599627370495.5, 4503599627370497, 9007199254740991, 9007199254740993, -4503599627370497,
		1e6, 1e20, 1e21, 1e-6, 1e-7, 1.5e-7, 123456789012345680000, 1.2345678901234568e21,
		math.MaxFloat64, math.SmallestNonzeroFloat64, 0.1, 0.2, 0.30000000000000004, 100, 1e300,
		5e-324, 2.2250738585072014e-308, 4.35, 0.000001, 0.0000012345, -1e21, -123.456,
	}
	for i := -30; i <= 30; i++ {
		p := math.Pow(10, float64(i))
		fs = append(fs, p, -p, p*1.5, math.Nextafter(p, 0), math.Nextafter(p, math.Inf(1)))
	}
	for range 20000 {
		fs = append(fs, math.Float64frombits(rng.Uint64()))
	}
	for range 5000 {
		fs = append(fs, float64(rng.Int64N(1<<62)-1<<61), float64(rng.IntN(2000000)-1000000)/float64(1+rng.IntN(1000)),
			float64(rng.IntN(200001)-100000)/2, rng.Float64()*math.Pow(10, float64(rng.IntN(60)-30)))
	}
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = bits(f)
	}
	return out
}

// keyLists is distinct keys in random insertion order, mixing array indices, near misses ("01",
// "-1", "4294967295", "1.0"), and strings whose UTF-8 and UTF-16 orders differ.
func keyLists(rng *rand.Rand) [][]string {
	pool := []string{
		"0", "1", "2", "9", "10", "100", "01", "00", "-1", "-0", "+1", "1.0", "1e3", " 1", "1 ",
		"4294967294", "4294967295", "4294967296", "99999999999", "path", "comment", "browseable",
		"read only", "guest ok", "Path", "a", "B", "_", "\uffff", "\U0001F600", "\ue000", "é", "",
		"vfs objects", "fruit:metadata", "create mask",
	}
	var out [][]string
	for range 300 {
		perm := rng.Perm(len(pool))
		n := rng.IntN(len(pool) + 1)
		keys := make([]string, n)
		for i := range n {
			keys[i] = pool[perm[i]]
		}
		out = append(out, keys)
	}
	return out
}
