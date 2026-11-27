package homelab

import (
	"reflect"
	"strings"
	"testing"
)

func intp(v int) *int { return &v }

// Ported from src/resources/group.test.ts: reading a group out of getent.

func TestParseGroupEntryReadsNameNumberAndMembers(t *testing.T) {
	got := parseGroupEntry("storage:x:1002:admin,player")
	want := &GroupEntry{Name: "storage", Gid: 1002, Members: []string{"admin", "player"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGroupEntryReadsAGroupWithNobodyInIt(t *testing.T) {
	// an empty member field is an empty list, not a list containing one empty name
	got := parseGroupEntry("readonly:x:1003:")
	want := &GroupEntry{Name: "readonly", Gid: 1003, Members: []string{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseGroupEntryReadsGidZero(t *testing.T) {
	got := parseGroupEntry("root:x:0:")
	if got == nil || got.Gid != 0 {
		t.Fatalf("got %+v, want gid 0", got)
	}
}

// The gid goes through Number(), as in the TypeScript: radix prefixes count, Go-only spellings do not.
func TestParseGroupEntryReadsTheGidAsNumberDoes(t *testing.T) {
	for line, want := range map[string]int{
		"g:x:0x10:":        16,
		"g:x:0o17:":        15,
		"g:x:0b1:":         1,
		"g:x: 1002 :":      1002,
		"g:x:\u00a01002:":  1002,
		"g:x::":            0, // Number('') is 0
		"\ufeffg:x:1002:a": 1002,
	} {
		got := parseGroupEntry(line)
		if got == nil || got.Gid != want {
			t.Errorf("parseGroupEntry(%q) = %+v, want gid %d", line, got, want)
		}
	}
	if got := parseGroupEntry("\ufeffg:x:1:"); got == nil || got.Name != "g" {
		t.Errorf("a leading BOM is trimmed as String.prototype.trim does, got %+v", got)
	}
}

func TestParseGroupEntrySaysNothingAboutALineThatIsNotAGroup(t *testing.T) {
	for _, line := range []string{"", "getent: command not found", "storage:x:notanumber:",
		"g:x:1_000:", "g:x:0x1p4:", "g:x:inf:", "g:x:Infinity:", "g:x:1e400:", "g:x:\u00851:"} {
		if got := parseGroupEntry(line); got != nil {
			t.Errorf("parseGroupEntry(%q) = %+v, want nil", line, got)
		}
	}
}

// Ported from src/resources/group.test.ts: refusing to renumber a group.

func TestRenumberRefusalRefusesAGidChange(t *testing.T) {
	refusal := renumberRefusal("storage", intp(1003), false, intp(1002))
	for _, part := range []string{"exists with gid 1002 and the code says 1003", "orphaned"} {
		if !strings.Contains(refusal, part) {
			t.Errorf("refusal %q does not contain %q", refusal, part)
		}
	}
}

func TestRenumberRefusalSaysWhatToRunAfterwards(t *testing.T) {
	refusal := renumberRefusal("storage", intp(1003), false, intp(1002))
	if !strings.Contains(refusal, "find / -xdev -gid 1002 -exec chgrp 1003") {
		t.Errorf("refusal %q does not say what to run", refusal)
	}
}

// The exact wording, since the message is the product.
func TestRenumberRefusalExactText(t *testing.T) {
	want := "storage exists with gid 1002 and the code says 1003. Renumbering does not chown anything, so every file owned by 1002 would be orphaned by a command that reports success. Set renumber: true if you mean it, and afterwards run `find / -xdev -gid 1002 -exec chgrp 1003 {} +` on every filesystem that matters."
	if got := renumberRefusal("storage", intp(1003), false, intp(1002)); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRenumberRefusalPermitsItWhenMeant(t *testing.T) {
	if got := renumberRefusal("storage", intp(1003), true, intp(1002)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRenumberRefusalNothingWhenGidMatches(t *testing.T) {
	if got := renumberRefusal("storage", intp(1002), false, intp(1002)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRenumberRefusalNothingWhenNoGidDeclared(t *testing.T) {
	// a group whose files nothing owns does not need one pinned
	if got := renumberRefusal("storage", nil, false, intp(1002)); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestRenumberRefusalNothingForAGroupThatDoesNotExistYet(t *testing.T) {
	if got := renumberRefusal("storage", intp(1002), false, nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// Escalation is src/ssh.ts escalateOn/asRoot/shellQuote.
func TestEscalation(t *testing.T) {
	cases := []struct{ escalation, want string }{
		{"sudo", `sudo -n sh -c 'groupadd '\''x'\'''`},
		{"none", `groupadd 'x'`},
		{"sudo-password", `sudo -S -p '' sh -c 'groupadd '\''x'\'''`},
	}
	for _, c := range cases {
		if got := escalate(c.escalation, "groupadd "+shellQuote("x")); got != c.want {
			t.Errorf("%s: got %s, want %s", c.escalation, got, c.want)
		}
	}
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Errorf("shellQuote: got %s", got)
	}
}
