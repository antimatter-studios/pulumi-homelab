package fstab

import (
	"reflect"
	"strings"
	"testing"
)

// The real fstab off the Pi, prose comments and all.
const real = `proc            /proc           proc    defaults          0       0
PARTUUID=00112233-01  /boot/firmware  vfat    defaults          0       2
PARTUUID=00112233-02  /               ext4    defaults,noatime  0       1
# a swapfile is not a swap partition, no line here
#   use  dphys-swapfile swap[on|off]  for that
UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults,noatime,compress=zstd,autodefrag,nofail  0  0
`

func noTarget(t *testing.T, line string) {
	t.Helper()
	if got, ok := TargetOf(line); ok {
		t.Errorf("TargetOf(%q) = %q, want none", line, got)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%q does not contain %q", s, sub)
	}
}

func TestFindEntry(t *testing.T) {
	t.Run("finds one by its target", func(t *testing.T) {
		if got, ok := TargetOf("UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults  0  0"); !ok || got != "/mnt/data" {
			t.Errorf("TargetOf = %q, %v", got, ok)
		}
		line, ok := FindEntry(real, "/mnt/data")
		if !ok {
			t.Fatal("no entry for /mnt/data")
		}
		contains(t, line, "btrfs")
	})
	t.Run("does not read a comment as an entry", func(t *testing.T) {
		noTarget(t, "#/dev/sda1 /mnt ext4 defaults 0 0")
		noTarget(t, "#   use  dphys-swapfile swap[on|off]  for that")
		if line, ok := FindEntry(real, "/mnt"); ok {
			t.Errorf("FindEntry(/mnt) = %q", line)
		}
	})
	t.Run("ignores blank lines and anything too short to be an entry", func(t *testing.T) {
		noTarget(t, "")
		noTarget(t, "   ")
		noTarget(t, "/dev/sda1")
	})
}

func TestUpsertFstab(t *testing.T) {
	t.Run("appends an entry the file does not have, keeping every other byte", func(t *testing.T) {
		line := Line(Entry{Source: "/mnt/data/k8s/podlogs", Target: "/var/log/pods", Type: "none", Options: []string{"bind", "nofail"}, Dump: 0, Pass: 0})
		updated := Upsert(real, "/var/log/pods", line)
		if !strings.HasPrefix(updated, strings.TrimSuffix(real, "\n")) {
			t.Errorf("did not keep the file: %q", updated)
		}
		contains(t, updated, "/mnt/data/k8s/podlogs /var/log/pods none bind,nofail 0 0")
	})
	t.Run("keeps the prose comments, which are the only documentation the machine has", func(t *testing.T) {
		updated := Upsert(real, "/var/log/pods", "a /var/log/pods none bind 0 0")
		contains(t, updated, "# a swapfile is not a swap partition, no line here")
		contains(t, updated, "#   use  dphys-swapfile swap[on|off]  for that")
	})
	t.Run("replaces in place rather than moving the entry to the end", func(t *testing.T) {
		updated := Upsert(real, "/mnt/data", "UUID=new  /mnt/data  btrfs  defaults  0  0")
		lines := strings.Split(updated, "\n")
		if lines[5] != "# pulumi-homelab" {
			t.Errorf("lines[5] = %q", lines[5])
		}
		if lines[6] != "UUID=new  /mnt/data  btrfs  defaults  0  0" {
			t.Errorf("lines[6] = %q", lines[6])
		}
		if want := len(strings.Split(real, "\n")) + 1; len(lines) != want {
			t.Errorf("%d lines, want %d", len(lines), want)
		}
	})
	t.Run("never touches the root filesystem entry while editing another", func(t *testing.T) {
		updated := Upsert(real, "/var/log/pods", "a /var/log/pods none bind 0 0")
		contains(t, updated, "PARTUUID=00112233-02  /               ext4    defaults,noatime  0       1")
	})
	t.Run("gives the same file whether it runs once or twice", func(t *testing.T) {
		line := "a /var/log/pods none bind 0 0"
		once := Upsert(real, "/var/log/pods", line)
		if twice := Upsert(once, "/var/log/pods", line); twice != once {
			t.Errorf("second run changed the file:\n%q\n%q", once, twice)
		}
	})
	t.Run("ends with exactly one newline however the file arrived", func(t *testing.T) {
		got := Upsert("a / ext4 defaults 0 0\n\n\n", "/x", "b /x ext4 defaults 0 0")
		if want := "a / ext4 defaults 0 0\n# pulumi-homelab\nb /x ext4 defaults 0 0\n"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestRemoveFromFstab(t *testing.T) {
	t.Run("removes the entry and nothing else", func(t *testing.T) {
		without := Remove(real, "/mnt/data")
		if strings.Contains(without, "00000000") {
			t.Errorf("entry still there: %q", without)
		}
		contains(t, without, "# a swapfile is not a swap partition, no line here")
		contains(t, without, "PARTUUID=00112233-02")
	})
	t.Run("leaves a commented entry for the same target alone", func(t *testing.T) {
		contains(t, Remove("#/dev/sda1 /mnt ext4 defaults 0 0\n", "/mnt"), "#/dev/sda1")
	})
}

func TestFstabLine(t *testing.T) {
	t.Run("writes the six fields in the order fstab has always had them", func(t *testing.T) {
		if got := Line(Entry{Source: "UUID=x", Target: "/mnt/x", Type: "ext4", Options: []string{"noatime"}, Dump: 0, Pass: 2}); got != "UUID=x /mnt/x ext4 noatime 0 2" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("writes defaults rather than an empty options field, which fstab cannot parse", func(t *testing.T) {
		if got := Line(Entry{Source: "a", Target: "/b", Type: "none", Options: []string{}, Dump: 0, Pass: 0}); got != "a /b none defaults 0 0" {
			t.Errorf("got %q", got)
		}
	})
}

func TestMarkedTargets(t *testing.T) {
	eq := func(t *testing.T, got, want []string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	t.Run("writes a marker above the entry, never on it", func(t *testing.T) {
		contains(t, Upsert(real, "/var/log/pods", "a /var/log/pods none bind 0 0"), "# pulumi-homelab\na /var/log/pods none bind 0 0")
	})
	t.Run("keeps one marker rather than growing another on every run", func(t *testing.T) {
		line := "a /var/log/pods none bind 0 0"
		once := Upsert(real, "/var/log/pods", line)
		twice := Upsert(once, "/var/log/pods", line)
		if twice != once {
			t.Errorf("second run changed the file")
		}
		if n := strings.Count(twice, "# pulumi-homelab"); n != 1 {
			t.Errorf("%d markers, want 1", n)
		}
	})
	t.Run("takes the marker away with the line", func(t *testing.T) {
		added := Upsert(real, "/var/log/pods", "a /var/log/pods none bind 0 0")
		if got := Remove(added, "/var/log/pods"); strings.Contains(got, "# pulumi-homelab") {
			t.Errorf("marker left behind: %q", got)
		}
	})
	t.Run("finds the mount points this provider claims", func(t *testing.T) {
		added := Upsert(real, "/var/log/pods", "a /var/log/pods none bind 0 0")
		eq(t, MarkedTargets(added), []string{"/var/log/pods"})
	})
	t.Run("does not claim a line somebody else wrote a comment above", func(t *testing.T) {
		eq(t, MarkedTargets(real), []string{})
		eq(t, MarkedTargets("# something else\na /x ext4 defaults 0 0"), []string{})
	})
	t.Run("adopting a hand-written line marks it rather than duplicating it", func(t *testing.T) {
		adopted := Upsert(real, "/mnt/data", "UUID=00000000-0000-0000-0000-000000000000  /mnt/data  btrfs  defaults  0  0")
		if n := strings.Count(adopted, "/mnt/data"); n != 1 {
			t.Errorf("/mnt/data appears %d times", n)
		}
		eq(t, MarkedTargets(adopted), []string{"/mnt/data"})
	})
}

// The TypeScript's answers under node: trim() and split(/\s+/) take a BOM as whitespace and a NEL
// as text, which strings.TrimSpace and strings.Fields do the other way round.
func TestFstabWhitespaceIsJavaScripts(t *testing.T) {
	eq := func(t *testing.T, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	for line, want := range map[string]string{
		"\ufeff#a /mnt c":    "",
		"\u0085#a /mnt c":    "/mnt",
		"a\u00a0/mnt\u3000c": "/mnt",
		"a\u0085/mnt c":      "",
		"\va\v/mnt\vc\v":     "/mnt",
	} {
		got, ok := TargetOf(line)
		if !ok {
			got = ""
		}
		if got != want {
			t.Errorf("TargetOf(%q) = %q, want %q", line, got, want)
		}
	}
	commented := "\ufeff#/dev/sda1 /mnt ext4 defaults 0 0\n"
	eq(t, Upsert(commented, "/mnt", "/dev/sdb1 /mnt ext4 defaults 0 0"),
		"\ufeff#/dev/sda1 /mnt ext4 defaults 0 0\n# pulumi-homelab\n/dev/sdb1 /mnt ext4 defaults 0 0\n")
	eq(t, Remove(commented, "/mnt"), commented)
	eq(t, MarkedTargets("\ufeff# pulumi-homelab\n/dev/sda1 /mnt/data ext4 defaults 0 2\n"), []string{"/mnt/data"})
}
