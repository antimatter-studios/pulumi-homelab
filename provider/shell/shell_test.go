package shell

import (
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	t.Run("wraps a plain word", func(t *testing.T) {
		if got := Quote("hello"); got != "'hello'" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("leaves the shell nothing to expand", func(t *testing.T) {
		for _, nasty := range []string{"$HOME", "`id`", "$(whoami)", "a && rm -rf /", "a; reboot", "*"} {
			if got := Quote(nasty); got != "'"+nasty+"'" {
				t.Errorf("Quote(%q) = %q", nasty, got)
			}
		}
	})
	t.Run("survives the one character that cannot live inside single quotes", func(t *testing.T) {
		if got := Quote("it's"); got != `'it'\''s'` {
			t.Errorf("got %q", got)
		}
		if got := Quote("'; rm -rf /; '"); !strings.Contains(got, `'\''`) {
			t.Errorf("got %q", got)
		}
	})
	t.Run("never leaves an unbalanced quote, whatever it is given", func(t *testing.T) {
		for _, text := range []string{"a'b", "''", "'", "a'b'c", ""} {
			q := Quote(text)
			if !strings.HasPrefix(q, "'") || !strings.HasSuffix(q, "'") {
				t.Errorf("Quote(%q) = %q is unbalanced", text, q)
			}
		}
	})
}
