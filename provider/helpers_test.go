package homelab

import (
	"testing"
)

func strp(s string) *string { return &s }

// Ported from src/ssh.ts heredoc: the chained commands go on the command line, before the body.
func TestHeredoc(t *testing.T) {
	if got := heredoc("/a b", "x", "chmod 0644 '/a b'"); got != "cat > '/a b' <<'PULUMI_EOF' && chmod 0644 '/a b'\nx\nPULUMI_EOF\n" {
		t.Errorf("%q", got)
	}
	if got := heredoc("/a", "x\n"); got != "cat > '/a' <<'PULUMI_EOF'\nx\nPULUMI_EOF\n" {
		t.Errorf("%q", got)
	}
}

// JSON.stringify, checked against what node prints for the same strings; a literal backslash-u in
// an anchor stays escaped (review of #98).
func TestJSONString(t *testing.T) {
	cases := [][2]string{
		{"case $- in", "\"case $- in\""},
		{"a\\u2028b", "\"a\\\\u2028b\""},
		{"a\u2028b", "\"a\u2028b\""},
		{"q\"\\\n\t\x01<>&", "\"q\\\"\\\\\\n\\t\\u0001<>&\""},
	}
	for _, c := range cases {
		if got := jsonString(c[0]); got != c[1] {
			t.Errorf("jsonString(%q) = %s, want %s", c[0], got, c[1])
		}
	}
}
