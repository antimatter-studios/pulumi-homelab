package jscompat

import (
	"bytes"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"testing"
)

// localeNames are names a declaration might carry: ASCII with every punctuation a user or group
// name allows, and Unicode that collates differently from code-point order (accents, precomposed
// and decomposed, fullwidth, other scripts, emoji, ligatures, case pairs).
var localeNames = []string{
	"alice", "Alice", "ALICE", "_svc", "svc", "svc2", "svc10", "svc-1", "svc.1", "svc_1", "svc$", "a", "b", "f", "z", "0", "9",
	"é", "é", "Élodie", "Élodie", "Åke", "Ångström", "ø", "Øyvind", "ß", "ss", "ñ", "ñ", "ı", "İ", "i",
	"Ａ", "ａｂｃ", "😀", "😀a", "中文", "日本", "한국", "Ωmega", "ωmega", "жена", "Жена", "عربي", "עברית", "ﬁle", "file",
	"æ", "ae", "œ", "Ð", "þ", " x", "x​y", "café", "cafe", "coöp", "coop", "naïve", "naive",
}

// TestLocaleCompareAgainstNode compares the sign of LocaleCompare with node's localeCompare for
// every ordered pair of keys built from localeNames, plus random ASCII names. It needs node on PATH
// (or $JSCOMPAT_NODE) and skips without it.
func TestLocaleCompareAgainstNode(t *testing.T) {
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
	keys := []string{}
	for _, name := range localeNames {
		keys = append(keys, "user:"+name, "group:"+name, "user:"+name+":x")
	}
	rng := rand.New(rand.NewPCG(46, 35))
	const ascii = "abcxyzABCXYZ0129_-.$"
	for range 200 {
		n := 1 + rng.IntN(6)
		b := make([]byte, n)
		for i := range b {
			b[i] = ascii[rng.IntN(len(ascii))]
		}
		keys = append(keys, "user:"+string(b))
	}
	in, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, "-e", `
const keys = JSON.parse(require("fs").readFileSync(0, "utf8"));
const out = [];
for (const a of keys) { const row = []; for (const b of keys) row.push(Math.sign(a.localeCompare(b))); out.push(row); }
process.stdout.write(JSON.stringify(out));`)
	cmd.Stdin = bytes.NewReader(in)
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var want [][]int
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	mismatches := 0
	for i, a := range keys {
		for j, b := range keys {
			if got := LocaleCompare(a, b); got != want[i][j] {
				mismatches++
				if mismatches <= 20 {
					t.Errorf("LocaleCompare(%q, %q) = %d, node says %d", a, b, got, want[i][j])
				}
			}
		}
	}
	if mismatches > 0 {
		t.Fatalf("%d of %d pairs disagree with node", mismatches, len(keys)*len(keys))
	}
}
