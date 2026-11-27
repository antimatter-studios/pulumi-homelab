// Package jscompat reproduces the JavaScript string and number semantics the TypeScript library
// relies on, so that the Go port renders and parses byte for byte what the TypeScript did.
//
// Every function here names the JavaScript operation it mirrors. Go's standard library is close to
// each of them and wrong at the edges: strings.TrimSpace trims U+0085 and keeps U+FEFF where
// String.prototype.trim does the opposite, strconv.ParseFloat accepts "inf", "nan", "1_000" and hex
// floats where Number() returns NaN, and strconv.FormatFloat writes "1e+06" where String() writes
// "1000000". Use these instead of the near misses, and add a case here rather than a private helper
// in a resource package.
//
// The differential test (jscompat_node_test.go) checks every function against node when one is on
// PATH, including every code point for the whitespace set and the case mappings.
package jscompat

import (
	"math"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
)

// IsSpace reports whether r is in JavaScript's whitespace set: what /\s/ matches and what
// String.prototype.trim removes (WhiteSpace plus LineTerminator). That is unicode.IsSpace plus
// U+FEFF (the byte order mark) and minus U+0085 (NEL).
func IsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680,
		0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// SpaceClass and NonSpaceClass are JavaScript's \s and \S as RE2 character classes. Go's \s is
// only [\t\n\f\r ] (no \v, NBSP, BOM or U+3000), and \S is its complement, so a pattern ported
// from TypeScript must spell them out: regexp.MustCompile(`^Remv` + SpaceClass + `+(` +
// NonSpaceClass + `+)`) for /^Remv\s+(\S+)/.
const (
	SpaceClass    = `[` + spaceSet + `]`
	NonSpaceClass = `[^` + spaceSet + `]`
	spaceSet      = `\t\n\v\f\r \x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`
)

// Trim is String.prototype.trim.
func Trim(s string) string { return strings.TrimFunc(s, IsSpace) }

// TrimStart is String.prototype.trimStart.
func TrimStart(s string) string { return strings.TrimLeftFunc(s, IsSpace) }

// TrimEnd is String.prototype.trimEnd.
func TrimEnd(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// Fields is s.trim().split(/\s+/), except that it returns no fields for a string that is all
// whitespace, where JavaScript returns [""]. Callers that relied on that one-element result should
// check Trim(s) == "" themselves.
func Fields(s string) []string { return strings.FieldsFunc(s, IsSpace) }

// Split is s.split(/\s+/) without the trim: leading or trailing whitespace yields an empty first or
// last field, exactly as in JavaScript. Split("") is [""].
func Split(s string) []string {
	out := []string{}
	start, inSpace := 0, false
	for i, r := range s {
		if IsSpace(r) {
			if !inSpace {
				out = append(out, s[start:i])
				inSpace = true
			}
			continue
		}
		if inSpace {
			start = i
			inSpace = false
		}
	}
	if inSpace {
		return append(out, "")
	}
	return append(out, s[start:])
}

var (
	decimalLiteral = regexp.MustCompile(`^[+-]?(?:Infinity|(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?)$`)
	radixLiteral   = regexp.MustCompile(`^0(?:[xX][0-9a-fA-F]+|[oO][0-7]+|[bB][01]+)$`)
)

// Number is Number(s) for a string: surrounding whitespace is trimmed, "" is 0, 0x/0o/0b integers
// are read in their radix (unsigned, as in JavaScript), "Infinity" may carry a sign, decimals and
// exponents read as usual, and anything else is NaN. No numeric separators, no "inf" or "nan", no
// hexadecimal floats.
func Number(s string) float64 {
	t := Trim(s)
	if t == "" {
		return 0
	}
	if radixLiteral.MatchString(t) {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[t[1]]
		n, _ := new(big.Int).SetString(t[2:], base)
		f, _ := new(big.Float).SetInt(n).Float64()
		return f
	}
	if !decimalLiteral.MatchString(t) {
		return math.NaN()
	}
	switch strings.TrimLeft(t, "+") {
	case "Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	// The pattern has already rejected everything ParseFloat accepts beyond JavaScript's grammar;
	// out-of-range values come back as ±Inf or ±0 with ErrRange, which is JavaScript's answer too.
	f, _ := strconv.ParseFloat(t, 64)
	return f
}

// FormatNumber is String(f) for a number (Number::toString with radix 10): the shortest digits
// that read back as f, written without an exponent when the decimal point falls within 21 digits
// of them and at most 6 places after it, otherwise as "1.5e+21" or "1e-7". -0 is "0".
func FormatNumber(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// 'e' with precision -1 gives the shortest round-tripping digits as d.ddde±XX.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	k, n := len(digits), x+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	m := digits[:1]
	if k > 1 {
		m += "." + digits[1:]
	}
	return sign + m + "e" + es + strconv.Itoa(abs(n-1))
}

func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}

// Round is Math.round: the nearest integer, halves rounding towards +Infinity, -0 for anything in
// [-0.5, -0]. Unlike math.Floor(f+0.5) it gets 0.49999999999999994 (0) and odd integers above 2^52
// (themselves) right.
func Round(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) || f == 0 {
		return f
	}
	if f < 0 && f >= -0.5 {
		return math.Copysign(0, -1)
	}
	r := math.Floor(f)
	if f-r >= 0.5 { // exact: f-r is f's fractional part, representable whenever f has one
		r++
	}
	return r
}

// IsArrayIndex reports whether a property key is an array index: the canonical decimal form of an
// integer in [0, 2^32-2]. JavaScript enumerates such keys first, in numeric order.
func IsArrayIndex(key string) bool {
	if key == "" || len(key) > 10 || (len(key) > 1 && key[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(key, 10, 64)
	return err == nil && n < math.MaxUint32 && strconv.FormatUint(n, 10) == key
}

// KeyOrder returns keys in the order JavaScript enumerates an object's own string keys
// (Object.keys, for...in, Object.entries, JSON.stringify): array-index keys first in ascending
// numeric order, then every other key in the order given, which is insertion order.
func KeyOrder(keys []string) []string {
	var idx, rest []string
	for _, k := range keys {
		if IsArrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.SliceStable(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 64)
		b, _ := strconv.ParseUint(idx[j], 10, 64)
		return a < b
	})
	return append(idx, rest...)
}

// SortedKeys returns m's keys in the order the TypeScript library saw them when m reached a dynamic
// provider. A dynamic provider's inputs cross Pulumi's engine as a protobuf Struct, whose map keys
// come out sorted, so by the time the TypeScript code enumerated an input object its insertion
// order was the sorted order: keys sorted by UTF-16 code units (JavaScript's default string
// comparison), then re-ordered by KeyOrder, which moves array-index keys ahead. A renderer fed an
// input map must therefore iterate SortedKeys, never Go's random map order or a byte-wise sort.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return CompareUTF16(keys[i], keys[j]) < 0 })
	return KeyOrder(keys)
}

// CompareUTF16 compares a and b the way JavaScript's < and the default Array.prototype.sort
// comparator do: by UTF-16 code units. It differs from Go's byte-wise comparison when a code point
// above U+FFFF (a surrogate pair, 0xD800-0xDFFF) meets one in U+E000-U+FFFF.
func CompareUTF16(a, b string) int {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			if ua[i] < ub[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(ua) < len(ub):
		return -1
	case len(ua) > len(ub):
		return 1
	}
	return 0
}

// ToLower is String.prototype.toLowerCase except for two context- and length-dependent mappings:
// U+0130 (İ) lowers to "i̇" (two code points) in JavaScript and to "i" here, and a capital sigma at
// the end of a word lowers to ς in JavaScript and to σ here. Characters whose case pairs are newer
// than the Unicode version Go ships (node 24 has Unicode 17, Go 1.26 has 15) are left alone. ASCII
// is exact.
func ToLower(s string) string { return strings.Map(unicode.ToLower, s) }

// ToUpper is String.prototype.toUpperCase, including the unconditional special casings that turn one
// code point into several (ß → "SS", ﬁ → "FI", ŉ → "ʼN" ...). The same Unicode-version caveat as
// ToLower applies. ASCII is exact.
func ToUpper(s string) string {
	var b strings.Builder
	for _, r := range s {
		if u, ok := upperSpecial[r]; ok {
			b.WriteString(u)
		} else {
			b.WriteRune(unicode.ToUpper(r))
		}
	}
	return b.String()
}
