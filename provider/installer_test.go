package homelab

import (
	"strings"
	"testing"
)

// Ported from src/resources/installer.test.ts: running a vendor installer as the account.
func TestAsAccount(t *testing.T) {
	t.Run("starts from the account's own home, not the caller's directory", func(t *testing.T) {
		command := asAccount("t3code", "/mnt/storage/t3code", "curl -fsSL https://example.com/install.sh | bash")
		for _, part := range []string{"runuser -u 't3code'", `cd "$HOME" && curl`, "HOME='/mnt/storage/t3code'"} {
			if !strings.Contains(command, part) {
				t.Errorf("%q does not contain %q", command, part)
			}
		}
	})
	t.Run("is exactly the TypeScript command", func(t *testing.T) {
		want := `runuser -u 'svc' -- env HOME='/home/svc' PATH='/home/svc/.local/bin:/usr/local/bin:/usr/bin:/bin' bash -c 'cd "$HOME" && x'`
		if got := asAccount("svc", "/home/svc", "x"); got != want {
			t.Fatalf("got %s", got)
		}
	})
}
