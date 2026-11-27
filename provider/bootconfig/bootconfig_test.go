package bootconfig

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const real = `# For more options and information see http://rptl.io/configtxt
dtparam=audio=on
camera_auto_detect=1

[all]
# PCIe / NVMe.
# NOTE: on the previous card these lived under [cm5], which only matches a
# Compute Module 5 - never a Pi 5 Model B - so they silently never applied.
dtparam=pciex1
#dtparam=pciex1_gen=3
dtoverlay=vc4-kms-v3d

[pi5]
arm_freq=2400

[cm5]
dtoverlay=dwc2,dr_mode=host
`

func section(text, name string) []string {
	return ParseBootSections(text)[name]
}

func in(t *testing.T, lines []string, want string) {
	t.Helper()
	if !slices.Contains(lines, want) {
		t.Errorf("%q does not contain %q", lines, want)
	}
}

func notIn(t *testing.T, lines []string, unwanted string) {
	t.Helper()
	if slices.Contains(lines, unwanted) {
		t.Errorf("%q contains %q", lines, unwanted)
	}
}

func TestParseBootSections(t *testing.T) {
	t.Run("puts lines before any header in the implicit all section", func(t *testing.T) {
		in(t, section(real, "all"), "dtparam=audio=on")
		in(t, section(real, "all"), "camera_auto_detect=1")
	})
	t.Run("keeps each section separate, because the same line means different things in each", func(t *testing.T) {
		in(t, section(real, "pi5"), "arm_freq=2400")
		in(t, section(real, "cm5"), "dtoverlay=dwc2,dr_mode=host")
		notIn(t, section(real, "pi5"), "dtparam=pciex1")
	})
	t.Run("has nothing to say about a section the file does not have", func(t *testing.T) {
		if lines, ok := ParseBootSections(real)["cm4"]; ok {
			t.Errorf("cm4 = %q", lines)
		}
	})
}

func TestKeyOf(t *testing.T) {
	t.Run("reads the key of a setting", func(t *testing.T) {
		for in, want := range map[string]string{"arm_freq=2400": "arm_freq", "  dtparam=pciex1  ": "dtparam"} {
			if got, ok := KeyOf(in); !ok || got != want {
				t.Errorf("KeyOf(%q) = %q, %v", in, got, ok)
			}
		}
	})
	t.Run("does not read a commented-out setting as one", func(t *testing.T) {
		for _, in := range []string{"#dtparam=pciex1_gen=3", "# NOTE: on the previous card these lived under [cm5]", "[pi5]", ""} {
			if got, ok := KeyOf(in); ok {
				t.Errorf("KeyOf(%q) = %q", in, got)
			}
		}
	})
}

func TestApplyToSection(t *testing.T) {
	t.Run("adds an overlay the section does not have", func(t *testing.T) {
		in(t, section(ApplyToSection(real, "all", nil, []string{"dtparam=nvme"}), "all"), "dtparam=nvme")
	})
	t.Run("does not touch a section it was not asked about", func(t *testing.T) {
		updated := ApplyToSection(real, "pi5", []Setting{{"arm_freq", "2000"}}, nil)
		in(t, section(updated, "pi5"), "arm_freq=2000")
		in(t, section(updated, "cm5"), "dtoverlay=dwc2,dr_mode=host")
		in(t, section(updated, "all"), "dtparam=pciex1")
	})
	t.Run("never collapses repeated overlay lines, which are separate hardware", func(t *testing.T) {
		all := section(ApplyToSection(real, "all", nil, []string{"dtoverlay=dwc2,dr_mode=host"}), "all")
		in(t, all, "dtoverlay=vc4-kms-v3d")
		in(t, all, "dtoverlay=dwc2,dr_mode=host")
	})
	t.Run("replaces a scalar setting in place rather than adding a second one", func(t *testing.T) {
		n := 0
		for _, line := range section(ApplyToSection(real, "pi5", []Setting{{"arm_freq", "2000"}}, nil), "pi5") {
			if key, ok := KeyOf(line); ok && key == "arm_freq" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%d arm_freq lines", n)
		}
	})
	t.Run("keeps the comments, which are the only record of why a line exists", func(t *testing.T) {
		updated := ApplyToSection(real, "all", nil, []string{"dtparam=nvme"})
		for _, want := range []string{"# NOTE: on the previous card these lived under [cm5]", "#dtparam=pciex1_gen=3"} {
			if !strings.Contains(updated, want) {
				t.Errorf("lost %q", want)
			}
		}
	})
	t.Run("gives the same file whether it runs once or twice", func(t *testing.T) {
		once := ApplyToSection(real, "all", nil, []string{"dtparam=pciex1"})
		if twice := ApplyToSection(once, "all", nil, []string{"dtparam=pciex1"}); twice != once {
			t.Errorf("twice %q != once %q", twice, once)
		}
	})
	t.Run("does nothing at all when the line is already there", func(t *testing.T) {
		if got := ApplyToSection(real, "all", nil, []string{"dtparam=pciex1"}); got != real {
			t.Errorf("got %q", got)
		}
	})
	t.Run("creates a section the file has never had rather than guessing where to put it", func(t *testing.T) {
		updated := ApplyToSection(real, "cm4", nil, []string{"dtoverlay=x"})
		in(t, section(updated, "cm4"), "dtoverlay=x")
		notIn(t, section(updated, "all"), "dtoverlay=x")
	})
}

func TestRemoveFromBootSection(t *testing.T) {
	t.Run("removes only what was named, from only the section named", func(t *testing.T) {
		without := RemoveFromBootSection(real, "all", nil, []string{"dtparam=pciex1"})
		notIn(t, section(without, "all"), "dtparam=pciex1")
		in(t, section(without, "all"), "dtoverlay=vc4-kms-v3d")
		in(t, section(without, "cm5"), "dtoverlay=dwc2,dr_mode=host")
	})
	t.Run("leaves the comments behind, because a note outlives the line it explains", func(t *testing.T) {
		without := RemoveFromBootSection(real, "all", nil, []string{"dtparam=pciex1"})
		if !strings.Contains(without, "# NOTE: on the previous card these lived under [cm5]") {
			t.Errorf("lost the note: %q", without)
		}
	})
}

func TestParseVcgencmd(t *testing.T) {
	t.Run("reads what vcgencmd resolved, which is the effective answer", func(t *testing.T) {
		want := map[string]string{"arm_freq": "2400", "total_mem": "8192"}
		if got := ParseVcgencmd("arm_freq=2400\ntotal_mem=8192\n"); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("says nothing on a machine that has no vcgencmd", func(t *testing.T) {
		if got := ParseVcgencmd(""); len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
}

func TestRenderSettings(t *testing.T) {
	render := func(t *testing.T, settings []Typed, unchecked []Setting, want []Setting) {
		t.Helper()
		if got := RenderSettings(settings, unchecked); !reflect.DeepEqual(got, want) {
			t.Errorf("RenderSettings = %v, want %v", got, want)
		}
	}
	t.Run("writes booleans as the 1 and 0 the firmware reads", func(t *testing.T) {
		render(t, []Typed{{"arm_boost", true}, {"disable_overscan", false}}, nil, []Setting{{"arm_boost", "1"}, {"disable_overscan", "0"}})
	})
	t.Run("writes numbers as themselves", func(t *testing.T) {
		render(t, []Typed{{"gpu_mem", 128}, {"hdmi_mode", 82}}, nil, []Setting{{"gpu_mem", "128"}, {"hdmi_mode", "82"}})
	})
	t.Run("spells dtparam=audio the way the file does, not the way the type does", func(t *testing.T) {
		render(t, []Typed{{"audio", "on"}}, nil, []Setting{{"dtparam=audio", "on"}})
	})
	t.Run("replaces dtparam=audio rather than adding a second one", func(t *testing.T) {
		all := section(ApplyToSection("[all]\ndtparam=audio=on\n", "all", RenderSettings([]Typed{{"audio", "off"}}, nil), nil), "all")
		in(t, all, "dtparam=audio=off")
		notIn(t, all, "dtparam=audio=on")
	})
	t.Run("does not let one key match another that merely starts the same way", func(t *testing.T) {
		all := section(ApplyToSection("[all]\narm_freq_min=600\n", "all", []Setting{{"arm_freq", "2400"}}, nil), "all")
		in(t, all, "arm_freq_min=600")
		in(t, all, "arm_freq=2400")
	})
	t.Run("leaves out what was never set, rather than writing a default nobody chose", func(t *testing.T) {
		render(t, []Typed{{"arm_boost", true}}, nil, []Setting{{"arm_boost", "1"}})
		render(t, nil, nil, nil)
	})
	t.Run("carries the unchecked settings through unaltered", func(t *testing.T) {
		render(t, nil, []Setting{{"pciex1_gen", "3"}}, []Setting{{"pciex1_gen", "3"}})
	})
	t.Run("lets an unchecked value override a typed one, visibly", func(t *testing.T) {
		render(t, []Typed{{"gpu_mem", 128}}, []Setting{{"gpu_mem", "256"}}, []Setting{{"gpu_mem", "256"}})
	})
	t.Run("is the only way to reach the settings that stop a machine booting", func(t *testing.T) {
		render(t, nil, []Setting{{"arm_freq", "3000"}, {"over_voltage", "6"}}, []Setting{{"arm_freq", "3000"}, {"over_voltage", "6"}})
	})
	t.Run("renders into the same shape the file comparison uses", func(t *testing.T) {
		updated := ApplyToSection("[pi5]\ngpu_mem=128\n", "pi5", RenderSettings([]Typed{{"gpu_mem", 76}}, nil), nil)
		in(t, section(updated, "pi5"), "gpu_mem=76")
	})
}

// Expectations below are the TypeScript's answers under node, with inputs passed through the
// sorted key order a protobuf Struct gives them.
func TestRenderInputWritesTheOrderTheTypeScriptProviderWrote(t *testing.T) {
	settings := map[string]any{"disable_overscan": true, "disable_fw_kms_setup": true, "audio": "on", "gpu_mem": float64(1000000), "hdmi_mode": math.Copysign(0, -1)}
	unchecked := map[string]string{"over_voltage": "2", "arm_freq": "2000", "7": "x", "disable_overscan": "5"}
	want := []Setting{
		{"7", "x"}, {"dtparam=audio", "on"}, {"disable_fw_kms_setup", "1"}, {"disable_overscan", "5"},
		{"gpu_mem", "1000000"}, {"hdmi_mode", "0"}, {"arm_freq", "2000"}, {"over_voltage", "2"},
	}
	for range 20 { // map order is random; every run must give the same list
		got := RenderInput(settings, unchecked)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("RenderInput = %q, want %q", got, want)
		}
	}
	got := ApplyToSection("[all]\n", "all", RenderInput(settings, unchecked), nil)
	if want := "[all]\n\n7=x\ndtparam=audio=on\ndisable_fw_kms_setup=1\ndisable_overscan=5\ngpu_mem=1000000\nhdmi_mode=0\narm_freq=2000\nover_voltage=2"; got != want {
		t.Errorf("ApplyToSection = %q, want %q", got, want)
	}
}

func TestBootConfigWhitespaceIsJavaScripts(t *testing.T) {
	for _, c := range []struct {
		name, text, section string
		settings            []Setting
		want                string
	}{
		{"a BOM before the first line does not hide its key", "\ufeffarm_64bit=0\n[pi4]\nx=1\n", "all", []Setting{{"arm_64bit", "1"}}, "arm_64bit=1\n[pi4]\nx=1\n"},
		{"a BOM before a heading does not hide the heading", "\ufeff[pi4]\nx=1\n", "pi4", []Setting{{"x", "2"}}, "\ufeff[pi4]\nx=2\n"},
		{"a vertical tab before a heading still ends the section", "[pi4]\nx=1\n\v[pi5]\ny=1\n", "pi4", []Setting{{"z", "3"}}, "[pi4]\nx=1\nz=3\n\v[pi5]\ny=1\n"},
		{"a NEL is part of the key, not space around it", "x\u0085=1\n", "all", []Setting{{"x", "2"}}, "x\u0085=1\n\nx=2"},
	} {
		if got := ApplyToSection(c.text, c.section, c.settings, nil); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
