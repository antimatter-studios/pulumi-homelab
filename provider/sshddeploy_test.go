package homelab

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
)

func TestSshdChangeChecksDeployLogin(t *testing.T) {
	const previous = "X11Forwarding yes\n"
	for _, c := range []struct {
		name   string
		input  map[string]string
		refuse string
	}{
		{"AllowUsers excludes deploy user", map[string]string{"AllowUsers": "root admin"}, "AllowUsers does not match"},
		{"DenyUsers includes deploy user", map[string]string{"DenyUsers": "root ch?is"}, "DenyUsers matches"},
		{"DenyUsers includes loopback", map[string]string{"DenyUsers": "chris@127.0.0.0/8"}, "DenyUsers matches"},
		{"AllowUsers excludes loopback", map[string]string{"AllowUsers": "chris@*,!127.0.0.0/8"}, "AllowUsers does not match"},
		{"AllowGroups excludes deploy groups", map[string]string{"AllowGroups": "root wheel"}, "AllowGroups does not match"},
		{"DenyGroups includes a supplementary group", map[string]string{"DenyGroups": "root dep*"}, "DenyGroups matches"},
		{"PubkeyAuthentication disabled", map[string]string{"PubkeyAuthentication": "no"}, "PubkeyAuthentication is not yes"},
		{"AuthorizedKeysFile moved", map[string]string{"AuthorizedKeysFile": "%h/.ssh/other .ssh/authorized_keys2"}, "AuthorizedKeysFile no longer includes"},
		{"AuthorizedKeysFile disabled", map[string]string{"AuthorizedKeysFile": "none"}, "AuthorizedKeysFile no longer includes"},
		{"public key login preserved", map[string]string{
			"AllowUsers": "root ch?is@LOCALHOST", "DenyUsers": "chris@192.0.2.0/24",
			"AllowGroups": "dep*", "DenyGroups": "wheel", "PubkeyAuthentication": "yes",
			"AuthorizedKeysFile": "%h/.ssh/authorized_keys /etc/ssh/keys/%u",
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newSshdBox(t)
			b.write(t, filepath.Join(b.directory, "90-tunnel.conf"), previous)
			s := b.server(t)
			_, err := s.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(c.input)})
			got, _ := b.dropIn()
			calls := b.calls(t)
			if c.refuse == "" {
				if err != nil || !strings.Contains(got, "AllowGroups dep*") || indexOf(calls, "systemctl reload", "") < 0 {
					t.Fatalf("safe drop-in was not installed: %v\n%s\n%v", err, got, calls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.refuse) || !strings.Contains(err.Error(), "nothing was changed") {
				t.Fatalf("expected refusal mentioning %q, got %v", c.refuse, err)
			}
			if got != previous || indexOf(calls, "systemctl ", "") >= 0 {
				t.Fatalf("refusal touched the service or drop-in: %q\n%v", got, calls)
			}
		})
	}
}

func TestSshdChangePreservesCustomDeployKeyPath(t *testing.T) {
	b := newSshdBox(t)
	b.write(t, b.config, "Include "+b.directory+"/*.conf\nAuthorizedKeysFile .ssh/%u-%U\n")
	keyDir := filepath.Join(b.root, "home/chris/.ssh")
	if err := os.Rename(filepath.Join(keyDir, "authorized_keys"), filepath.Join(keyDir, "chris-1001")); err != nil {
		t.Fatal(err)
	}
	s := b.server(t)
	if _, err := s.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(map[string]string{
		"AuthorizedKeysFile": "%h/.ssh/chris-1001",
	})}); err != nil {
		t.Fatal(err)
	}
}

func TestSshdChangeRefusalLeavesMissingDropInDirectoryAlone(t *testing.T) {
	b := newSshdBox(t)
	if err := os.Remove(b.directory); err != nil {
		t.Fatal(err)
	}
	s := b.server(t)
	_, err := s.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(map[string]string{"PubkeyAuthentication": "no"})})
	if err == nil || !strings.Contains(err.Error(), "PubkeyAuthentication is not yes") {
		t.Fatalf("expected authentication refusal, got %v", err)
	}
	if _, err := os.Stat(b.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal touched drop-in directory: %v", err)
	}
}
