package homelab

import (
	"testing"
)

func TestAuthorizedReadParsing(t *testing.T) {
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnop laptop"
	out := "700\n600 chris\n#pulumi-homelab#\nrestrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnop old-name\n"
	got := parseAuthorizedRead(out, key)
	if got.line == nil || *got.line != "restrict ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnop old-name" ||
		got.mode != "0600" || got.directoryMode != "0700" || got.owner != "chris" {
		t.Errorf("%+v", got)
	}
	none := parseAuthorizedRead("700\nnone none\n#pulumi-homelab#\n", key)
	if none.line != nil || none.mode != "" || none.owner != "" {
		t.Errorf("%+v", none)
	}
}
