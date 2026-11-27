package homelab

import (
	"testing"
)

// Ported from src/resources/instance.test.ts: naming the unit.

func TestInstanceUnit(t *testing.T) {
	for _, c := range [][4]string{
		{"avahi-alias", "photos.example.local", "service", "avahi-alias@photos.example.local.service"},
		{"backup", "mnt/data", "service", "backup@mnt-data.service"},
		{"systemd-backlight", "leds:x", "mount", "systemd-backlight@leds:x.mount"},
	} {
		if got := instanceUnit(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("got %s, want %s", got, c[3])
		}
	}
	if got := parseInstanceRead("enabled\n#pulumi-homelab#\nactive\n"); !got.enabled || !got.started {
		t.Errorf("%+v", got)
	}
}
