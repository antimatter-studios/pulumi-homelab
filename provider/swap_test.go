package homelab

import (
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/swap"
)

func sp(s string) *string { return &s }

// Ported from src/resources/swap.test.ts: deciding whether anything needs doing.
func TestSwapAction(t *testing.T) {
	off := swapBefore{dphys: sp("masked")}
	on := swapBefore{active: []swap.Active{{Path: "/var/swap", SizeMB: 2048}}, fstab: []string{"/var/swap none swap sw 0 0"}, dphys: sp("masked")}
	onNoFstab := on
	onNoFstab.fstab = nil
	cases := []struct {
		name   string
		before swapBefore
		wanted swapWanted
		want   string
	}{
		{"does nothing when swap is already off in both tenses", off, swapWanted{path: "/var/swap"}, "nothing"},
		{"does nothing when swap is already on at the size asked for", on, swapWanted{enabled: true, sizeMb: intp(2048), path: "/var/swap"}, "nothing"},
		{"disables when something is swapping now", on, swapWanted{path: "/var/swap"}, "disable"},
		{"disables when nothing is swapping now but fstab will bring it back", swapBefore{fstab: []string{"/var/swap none swap sw 0 0"}}, swapWanted{path: "/var/swap"}, "disable"},
		{"disables when the unit is merely disabled rather than masked", swapBefore{dphys: sp("disabled")}, swapWanted{path: "/var/swap"}, "disable"},
		{"does nothing when the machine never had the package at all", swapBefore{}, swapWanted{path: "/var/swap"}, "nothing"},
		{"enables when the size differs from what is mounted", on, swapWanted{enabled: true, sizeMb: intp(4096), path: "/var/swap"}, "enable"},
		{"enables when swap is on but nothing will bring it back", onNoFstab, swapWanted{enabled: true, sizeMb: intp(2048), path: "/var/swap"}, "enable"},
		{"enables when something else is swapping but not the declared file", swapBefore{active: []swap.Active{{Path: "/other", SizeMB: 2048}}, fstab: []string{"x"}},
			swapWanted{enabled: true, sizeMb: intp(2048), path: "/var/swap"}, "enable"},
		{"accepts any size when none was asked for", on, swapWanted{enabled: true, path: "/var/swap"}, "nothing"},
	}
	for _, c := range cases {
		if got := swapAction(c.before, c.wanted); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// Ported from src/resources/swap.test.ts: leaving swap declared elsewhere alone.
func TestSwapActionForeign(t *testing.T) {
	elsewhere := func() swapBefore {
		return swapBefore{
			active: []swap.Active{{Path: "/dev/zram0", SizeMB: 8192}, {Path: "/mnt/storage/swap/swap-1", SizeMB: 1024}},
			fstab:  []string{"/mnt/storage/swap/swap-1 none swap pri=10,nofail 0 0"},
			dphys:  sp("masked"),
		}
	}
	allow := swapWanted{path: "/var/swap", otherSwap: "allow"}
	swapping := elsewhere()
	swapping.active = append(swapping.active, swap.Active{Path: "/var/swap", SizeMB: 100})
	returning := elsewhere()
	returning.fstab = append(returning.fstab, "/var/swap none swap sw 0 0")
	disabled := elsewhere()
	disabled.dphys = sp("disabled")
	enabled := elsewhere()
	enabled.active, enabled.fstab = []swap.Active{{Path: "/var/swap", SizeMB: 2048}}, []string{"/var/swap none swap sw 0 0"}
	cases := []struct {
		name   string
		before swapBefore
		wanted swapWanted
		want   string
	}{
		{"does nothing when only other swap is running and the unit is masked", elsewhere(), allow, "nothing"},
		{"still reads the same machine as swap to switch off without the option", elsewhere(), swapWanted{path: "/var/swap"}, "disable"},
		{"disables when the declared path is swapping", swapping, allow, "disable"},
		{"disables when fstab will bring the declared path back", returning, allow, "disable"},
		{"disables when the unit is only disabled, so an upgrade could put swap on the card", disabled, allow, "disable"},
		{"is ignored when swap is enabled", enabled, swapWanted{enabled: true, sizeMb: intp(2048), path: "/var/swap", otherSwap: "allow"}, "nothing"},
	}
	for _, c := range cases {
		if got := swapAction(c.before, c.wanted); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestParseSwapRead(t *testing.T) {
	out := "Filename\tType\tSize\tUsed\tPriority\n/dev/zram0 partition 8388604 0 100\n#pulumi-homelab#fstab\n" +
		"/var/swap none swap sw 0 0\n#pulumi-homelab#dphys\nmasked\n#pulumi-homelab#unit\ndphys-swapfile.service masked enabled\n#pulumi-homelab#zram\nyes\n"
	got := parseSwapRead(out, "/var/swap")
	if len(got.Active) != 1 || got.Active[0].SizeMb != 8192 || len(got.Fstab) != 1 || got.Dphys == nil || *got.Dphys != "masked" || !got.Zram {
		t.Errorf("%+v", got)
	}
	if absent := parseSwapRead("Filename\n#pulumi-homelab#fstab\n#pulumi-homelab#dphys\n#pulumi-homelab#unit\n#pulumi-homelab#zram\nno\n", "/var/swap"); absent.Dphys != nil || absent.Zram {
		t.Errorf("%+v", absent)
	}
}

func TestSwapDisableCommandOnlyTouchesTheDeclaredPathWhenOthersAreAllowed(t *testing.T) {
	got := swapDisableCommand(sp("masked"), "dphys-swapfile", "/etc/fstab", sp("/var/swap"))
	want := `swapoff '/var/swap' 2>/dev/null || true; awk -v only='/var/swap' 'BEGIN{OFS=FS=" "} /^[[:space:]]*#/ {print; next} $3 == "swap" && (only == "" || $1 == only) {print "#" $0; next} {print}' '/etc/fstab' > '/etc/fstab.pulumi' && mv '/etc/fstab.pulumi' '/etc/fstab'; systemctl disable --now 'dphys-swapfile' || true; systemctl mask 'dphys-swapfile' || true`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
