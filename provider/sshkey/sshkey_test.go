package sshkey

import (
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestParseFingerprint(t *testing.T) {
	check := func(t *testing.T, in, want string) {
		t.Helper()
		if got := ParseFingerprint(in); got != want {
			t.Errorf("ParseFingerprint(%q) = %q, want %q", in, got, want)
		}
	}
	t.Run("takes the fingerprint out of ssh-keygen’s line", func(t *testing.T) {
		check(t, "256 SHA256:8Zc1qgJ+EXAMPLEfingerprintEXAMPLEabcdef admin@host (ED25519)", "SHA256:8Zc1qgJ+EXAMPLEfingerprintEXAMPLEabcdef")
	})
	t.Run("is unmoved by a comment containing spaces", func(t *testing.T) {
		check(t, "3072 SHA256:abc123 a comment with spaces (RSA)", "SHA256:abc123")
	})
	t.Run("answers empty rather than throwing when there is nothing to read", func(t *testing.T) {
		check(t, "", "")
	})
}

func TestParseHostKeys(t *testing.T) {
	real := strings.Join([]string{
		"/etc/ssh/ssh_host_ecdsa_key.pub 256 SHA256:aaaEXAMPLEecdsa root@host (ECDSA)",
		"/etc/ssh/ssh_host_ed25519_key.pub 256 SHA256:bbbEXAMPLEed25519 root@host (ED25519)",
		"/etc/ssh/ssh_host_rsa_key.pub 3072 SHA256:cccEXAMPLErsa root@host (RSA)",
	}, "\n")
	t.Run("names each key by its algorithm rather than by its path", func(t *testing.T) {
		var keys []string
		for k := range ParseHostKeys(real) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if want := []string{"ecdsa", "ed25519", "rsa"}; !reflect.DeepEqual(keys, want) {
			t.Errorf("got %v, want %v", keys, want)
		}
	})
	t.Run("reads the fingerprint of each", func(t *testing.T) {
		if got := ParseHostKeys(real)["ed25519"]; got != "SHA256:bbbEXAMPLEed25519" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("skips a key ssh-keygen could not read rather than recording an empty one", func(t *testing.T) {
		if got := ParseHostKeys("/etc/ssh/ssh_host_dsa_key.pub\n"); !reflect.DeepEqual(got, map[string]string{}) {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("says nothing on a machine with no host keys at all", func(t *testing.T) {
		if got := ParseHostKeys(""); !reflect.DeepEqual(got, map[string]string{}) {
			t.Errorf("got %#v", got)
		}
	})
}
