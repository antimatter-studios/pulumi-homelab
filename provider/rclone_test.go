package homelab

import (
	"reflect"
	"testing"
)

// Ported from src/resources/rclone.test.ts: building the command.
func TestConfigPairs(t *testing.T) {
	t.Run("writes key and value as separate arguments, as config create takes them", func(t *testing.T) {
		if got := configPairs(map[string]string{"host": "far.example", "user": "admin"}); !reflect.DeepEqual(got, []string{"host", "far.example", "user", "admin"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("sorts, so a rearranged settings map is not a different command", func(t *testing.T) {
		a := configPairs(map[string]string{"user": "admin", "host": "far.example"})
		b := configPairs(map[string]string{"host": "far.example", "user": "admin"})
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%v != %v", a, b)
		}
	})
	t.Run("keeps an empty value rather than dropping the key", func(t *testing.T) {
		if got := configPairs(map[string]string{"pass": ""}); !reflect.DeepEqual(got, []string{"pass", ""}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("has nothing to say about an empty map", func(t *testing.T) {
		if got := configPairs(map[string]string{}); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

// The create command as the TypeScript writes it, including the double space an empty map leaves.
func TestRcloneCreateCommand(t *testing.T) {
	got := rcloneCreateCommand("/srv/rclone.conf", "archive", "sftp", map[string]string{"user": "admin", "host": "h"}, map[string]string{"pass": "p"})
	want := "mkdir -p '/srv' && rclone --config '/srv/rclone.conf' config create 'archive' 'sftp' 'host' 'h' 'user' 'admin' 'pass' 'p' --obscure --non-interactive >/dev/null"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	got = rcloneCreateCommand("rclone.conf", "a", "s3", nil, nil)
	want = "mkdir -p 'rclone.conf' && rclone --config 'rclone.conf' config create 'a' 's3'  --obscure --non-interactive >/dev/null"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}
