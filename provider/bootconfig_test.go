package homelab

import (
	"reflect"
	"strings"
	"testing"
)

// Ported from src/resources/bootconfig.test.ts: checking overlay names against what the machine has.
func TestUnknownOverlays(t *testing.T) {
	available := []string{"vc4-kms-v3d", "dwc2", "disable-bt", "pi3-disable-wifi"}
	if got := overlayNameOf("dtoverlay=vc4-kms-v3d"); got != "vc4-kms-v3d" {
		t.Errorf("reads the overlay a line asks for: %q", got)
	}
	if got := overlayNameOf("dtoverlay=dwc2,dr_mode=host"); got != "dwc2" {
		t.Errorf("stops at the first comma: %q", got)
	}
	for _, line := range []string{"dtparam=pciex1", "arm_boost=1", "# dtoverlay=vc4-kms-v3d"} {
		if got := overlayNameOf(line); got != "" {
			t.Errorf("says nothing about %q: %q", line, got)
		}
	}
	if got := unknownOverlays([]string{"dtoverlay=dwc3"}, available); !reflect.DeepEqual(got, []string{"dwc3"}) {
		t.Errorf("catches the typo: %v", got)
	}
	if got := unknownOverlays([]string{"dtoverlay=vc4-kms-v3d", "dtoverlay=dwc2,dr_mode=host"}, available); len(got) != 0 {
		t.Errorf("passes every name the machine has: %v", got)
	}
	if got := unknownOverlays([]string{"dtparam=pciex1", "dtparam=audio=on"}, available); len(got) != 0 {
		t.Errorf("no opinion about lines that are not overlays: %v", got)
	}
}

func TestBootRenderedUsesTheProductionOrder(t *testing.T) {
	on, mem := true, 128.0
	ordered, settings := bootRendered(BootConfigArgs{Settings: &BootSettings{Audio: strp("on"), Arm64bit: &on, GpuMem: &mem}, Unchecked: map[string]string{"z": "1", "b": "2"}})
	var keys []string
	for _, s := range ordered {
		keys = append(keys, s.Key+"="+s.Value)
	}
	if got := strings.Join(keys, " "); got != "arm_64bit=1 dtparam=audio=on gpu_mem=128 b=2 z=1" {
		t.Errorf("%s", got)
	}
	if settings["dtparam=audio"] != "on" {
		t.Errorf("%v", settings)
	}
}

func TestBootFromSection(t *testing.T) {
	settings, overlays := bootFromSection([]string{"# PCIe", "dtparam=pciex1", "arm_freq=2400", "dtoverlay=dwc2,dr_mode=host", "dtoverlay=a", "dtoverlay=a", ""})
	if !reflect.DeepEqual(settings, map[string]string{"arm_freq": "2400"}) ||
		!reflect.DeepEqual(overlays, []string{"dtparam=pciex1", "dtoverlay=dwc2,dr_mode=host", "dtoverlay=a", "dtoverlay=a"}) {
		t.Errorf("%v %v", settings, overlays)
	}
}
