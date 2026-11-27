package authkeys

import (
	"strings"
	"testing"
)

const (
	key   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH0k9tBqZ3rEXAMPLEexampleEXAMPLEkey0 someone@laptop"
	body  = "AAAAC3NzaC1lZDI1NTE5AAAAIH0k9tBqZ3rEXAMPLEexampleEXAMPLEkey0"
	other = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDfoREXAMPLEotherkeyEXAMPLE1 build@ci"
)

func bodyOf(line string) string {
	b, _ := KeyBody(line)
	return b
}

func hasBody(t *testing.T, line, want string) {
	t.Helper()
	if got, ok := KeyBody(line); !ok || got != want {
		t.Errorf("KeyBody(%q) = %q, %v, want %q", line, got, ok, want)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%q does not contain %q", s, sub)
	}
}

func countBody(text, want string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if b, ok := KeyBody(line); ok && b == want {
			n++
		}
	}
	return n
}

func TestKeyBody(t *testing.T) {
	t.Run("finds the body of a plain key line", func(t *testing.T) {
		hasBody(t, key, body)
	})
	t.Run("finds it past the options, which may contain spaces inside quotes", func(t *testing.T) {
		hasBody(t, `from="10.0.0.0/24" command="/usr/bin/x --y" `+key, body)
	})
	t.Run("is not fooled by a renamed laptop", func(t *testing.T) {
		hasBody(t, strings.Replace(key, "someone@laptop", "someone@newlaptop", 1), bodyOf(key))
	})
	t.Run("says nothing about a comment or a blank line", func(t *testing.T) {
		for _, line := range []string{"# an old key, removed 2024", "", "not a key at all"} {
			if got, ok := KeyBody(line); ok {
				t.Errorf("KeyBody(%q) = %q, want none", line, got)
			}
		}
	})
}

func TestAuthorizedLine(t *testing.T) {
	t.Run("puts the options before the key, comma-separated", func(t *testing.T) {
		if got, want := AuthorizedLine(key, []string{`from="10.0.0.0/24"`, "restrict"}), `from="10.0.0.0/24",restrict `+key; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("writes a bare key when there are no options", func(t *testing.T) {
		if got := AuthorizedLine(key, nil); got != key {
			t.Errorf("got %q", got)
		}
	})
}

func TestUpsertAuthorized(t *testing.T) {
	t.Run("adds a key the file does not have, keeping the others", func(t *testing.T) {
		updated := UpsertAuthorized(other+"\n", key, AuthorizedLine(key, nil))
		contains(t, updated, other)
		lines := strings.Split(updated, "\n")
		hasBody(t, lines[1], body)
	})
	t.Run("replaces the line for a key already there rather than adding a second", func(t *testing.T) {
		once := UpsertAuthorized("", key, AuthorizedLine(key, nil))
		if twice := UpsertAuthorized(once, key, AuthorizedLine(key, nil)); twice != once {
			t.Errorf("got %q, want %q", twice, once)
		}
	})
	t.Run("tightens options in place, leaving no unrestricted copy underneath", func(t *testing.T) {
		open := UpsertAuthorized("", key, AuthorizedLine(key, nil))
		restricted := UpsertAuthorized(open, key, AuthorizedLine(key, []string{`from="10.0.0.0/24"`}))
		if n := countBody(restricted, body); n != 1 {
			t.Errorf("%d lines carry the key, want 1", n)
		}
		contains(t, restricted, `from="10.0.0.0/24"`)
	})
	t.Run("matches on the body even when the comment changed", func(t *testing.T) {
		existing := strings.Replace(key, "someone@laptop", "someone@oldlaptop", 1) + "\n"
		updated := UpsertAuthorized(existing, key, AuthorizedLine(key, nil))
		if n := countBody(updated, body); n != 1 {
			t.Errorf("%d lines carry the key, want 1", n)
		}
		contains(t, updated, "someone@laptop")
	})
}

func TestRemoveAuthorized(t *testing.T) {
	t.Run("removes that key and leaves everybody else’s alone", func(t *testing.T) {
		without := RemoveAuthorized(other+"\n"+key+"\n", key)
		contains(t, without, other)
		hasBody(t, without, bodyOf(other))
	})
	t.Run("leaves a file alone when the key is not in it", func(t *testing.T) {
		contains(t, RemoveAuthorized(other+"\n", key), other)
	})
}

func TestKeyComment(t *testing.T) {
	check := func(t *testing.T, line, want string) {
		t.Helper()
		if got := KeyComment(line); got != want {
			t.Errorf("KeyComment(%q) = %q, want %q", line, got, want)
		}
	}
	t.Run("reports the comment, which is the only human-readable part", func(t *testing.T) {
		check(t, key, "someone@laptop")
		check(t, other, "build@ci")
	})
	t.Run("reads the comment past the options", func(t *testing.T) {
		check(t, `from="10.0.0.0/24" `+key, "someone@laptop")
	})
	t.Run("says nothing rather than guessing for a key with no comment", func(t *testing.T) {
		check(t, strings.Replace(key, " someone@laptop", "", 1), "")
	})
	t.Run("handles a comment containing spaces, which ssh-keygen allows", func(t *testing.T) {
		check(t, key+" and more words", "someone@laptop and more words")
	})
	t.Run("leaves the other owner’s key alone when this one is removed", func(t *testing.T) {
		check(t, strings.TrimSpace(RemoveAuthorized(key+"\n"+other+"\n", key)), "build@ci")
	})
}
