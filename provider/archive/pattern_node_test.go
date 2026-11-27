package archive

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// oracle answers, for each pattern and text, whether Node compiles the pattern as an unflagged
// RegExp and what text.match(new RegExp(pattern))?.[1] ?? "" is: what the TypeScript library's
// parseVersion reads.
const oracle = `
const cases = JSON.parse(require("fs").readFileSync(0, "utf8"));
process.stdout.write(JSON.stringify(cases.map(([pattern, text]) => {
  let re;
  try { re = new RegExp(pattern); } catch { return { compiles: false, got: "" }; }
  const m = text.match(re);
  return { compiles: true, got: (m && m[1] !== undefined) ? m[1] : "" };
})));`

// TestPatternsAgainstNode compares ParseVersion with Node over patterns and texts chosen to hit the
// places RE2 and JavaScript differ: characters outside the Basic Multilingual Plane, private-use
// characters, JavaScript's whitespace, line terminators, and class edge cases.
// Every pattern Node rejects must be refused here; every pattern accepted here must read every
// text as Node does. It needs node on PATH (or in $JSCOMPAT_NODE) and skips without it.
func TestPatternsAgainstNode(t *testing.T) {
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
	patterns := []string{
		DefaultVersionPattern, `^.([0-9]+)$`, `^..([0-9]+)$`, `^(.)$`, `^(.)(.)$`, `(.)`, `v(.)`, `v([^a]+)`,
		`(\S+)`, `(\s+)`, `(\W+)`, `(\D+)`, `([^\s]+)`, `x(.*)y`, `([^]*)`, `x([]])y`, `^([\x00-\xff]+)`,
		`(?<a1>[0-9]+)`, `(?=v)([0-9]+)`, `(a)\1`,
		`\b([0-9]+)\b`, `(\w+)$`, `^(\d+)`, `(?:go)?(\S+)`, `([0-9`, `é(\d)`, `😀(\d+)`,
	}
	texts := []string{
		"widget 1.4.2", "😀42", "a42", "", "", "\U000f0000", "\U000f0000\U000f0400",
		"v😀", "v1.2😀 x", "1.2 3", "1.2　x", "x y", "x\r\ny", "xyz", "]", "\ufeff1.0",
		"é1", "go1.22.3 linux/amd64", "Exec format error", "", "xy", "\U0010ffff 9",
	}
	type row = [2]string
	var cases []row
	for _, p := range patterns {
		for _, text := range texts {
			cases = append(cases, row{p, text})
		}
	}
	in, _ := json.Marshal(cases)
	cmd := exec.Command(node, "-e", oracle)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var answers []struct {
		Compiles bool   `json:"compiles"`
		Got      string `json:"got"`
	}
	if err := json.Unmarshal(out, &answers); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		pattern, text, want := c[0], c[1], answers[i]
		refused := PatternRefusal(pattern) != ""
		if !want.Compiles {
			if !refused {
				t.Errorf("Node rejects %q, but it is not refused", pattern)
			}
			continue
		}
		if refused {
			continue // refusing a pattern Node accepts is allowed; reading it differently is not
		}
		if got := ParseVersion(text, pattern); got != want.Got {
			t.Errorf("ParseVersion(%q, %q) = %q, Node reads %q", text, pattern, got, want.Got)
		}
	}
}
