package apt

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/aptmark"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseInstalled(t *testing.T) {
	out := strings.Join([]string{
		"btop installed 1.2.13-1",
		"ripgrep installed 13.0.0-4+b2",
		"player deinstall 2:20.2+dfsg-1",
		"libc6:arm64 installed 2.36-9+rpt2+deb12u7",
	}, "\n")
	t.Run("reads the installed ones with their versions", func(t *testing.T) {
		installed := ParseInstalled(out)
		eq(t, installed["btop"], "1.2.13-1")
		eq(t, installed["ripgrep"], "13.0.0-4+b2")
	})
	t.Run("does not count a package that is known but removed", func(t *testing.T) {
		if v, ok := ParseInstalled(out)["player"]; ok {
			t.Errorf("player read as installed at %q", v)
		}
	})
	t.Run("says nothing about a package dpkg has never heard of", func(t *testing.T) {
		eq(t, ParseInstalled(""), map[string]string{})
	})
}

func TestParseSimulatedRemovals(t *testing.T) {
	simulation := `NOTE: This is only a simulation!
Reading package lists...
Building dependency tree...
The following packages will be REMOVED:
  player player-data player-plugin
Remv player [2:20.2+dfsg-1]
Remv player-data [2:20.2+dfsg-1]
Remv player-plugin [20.3.7-1]
`
	t.Run("names every package apt would remove", func(t *testing.T) {
		eq(t, ParseSimulatedRemovals(simulation), []string{"player", "player-data", "player-plugin"})
	})
	t.Run("does not read the prose above it as packages", func(t *testing.T) {
		got := ParseSimulatedRemovals(simulation)
		if slices.Contains(got, "The") {
			t.Errorf("read prose as a package: %q", got)
		}
		eq(t, len(got), 3)
	})
	t.Run("says nothing when apt would remove nothing", func(t *testing.T) {
		eq(t, ParseSimulatedRemovals("NOTE: This is only a simulation!\nReading package lists...\n"), []string{})
	})
	t.Run("reads a multi-arch name, which is what gets compared against the request", func(t *testing.T) {
		eq(t, ParseSimulatedRemovals("Remv libc6:arm64 [2.36-9]"), []string{"libc6:arm64"})
	})
}

func TestParsePackageState(t *testing.T) {
	out := strings.Join([]string{
		"ovmf installed 2024.02",
		"qemu-efi-arm installed 2024.02",
		"#pulumi-homelab#manual",
		"qemu-efi-arm",
	}, "\n")
	t.Run("splits the two answers apart", func(t *testing.T) {
		eq(t, ParsePackageState(out), PackageState{
			Installed: map[string]string{"ovmf": "2024.02", "qemu-efi-arm": "2024.02"},
			Manual:    []string{"qemu-efi-arm"},
		})
	})
	t.Run("does not fold the marker into either answer", func(t *testing.T) {
		state := ParsePackageState(out)
		if _, ok := state.Installed["#pulumi-homelab#manual"]; ok {
			t.Error("marker read as an installed package")
		}
		if slices.Contains(state.Manual, "#pulumi-homelab#manual") {
			t.Error("marker read as a manual package")
		}
	})
	t.Run("reads a machine that holds none of them as manual", func(t *testing.T) {
		eq(t, ParsePackageState("ovmf installed 1\n#pulumi-homelab#manual\n").Manual, []string{})
	})
	t.Run("still reads dpkg when apt-mark said nothing at all", func(t *testing.T) {
		eq(t, ParsePackageState("ovmf installed 1\n").Installed, map[string]string{"ovmf": "1"})
	})
	t.Run("finds the drift the old read could not see", func(t *testing.T) {
		state := ParsePackageState(out)
		eq(t, aptmark.AutoMarked([]string{"ovmf", "qemu-efi-arm"}, state.Installed, state.Manual), []string{"ovmf"})
	})
}
