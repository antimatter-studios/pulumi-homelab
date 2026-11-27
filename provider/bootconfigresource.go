package homelab

import (
	"context"
	"fmt"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/bootconfig"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/resolved"
	"github.com/antimatter-studios/pulumi-homelab/provider/same"
)

// BootConfig is lines in one filter section of a Raspberry Pi's config.txt. Ported from
// src/resources/bootconfig.ts. Overlay names are checked against the overlays the machine has,
// settings render in the order the TypeScript wrote them (bootconfig.RenderInput), and delete
// removes the declared keys and overlay lines from the section.
type BootConfig struct{}

// BootSettings are the typed settings, named as config.txt names them.
type BootSettings struct {
	Arm64bit            *bool `pulumi:"arm_64bit,optional"`
	ArmBoost            *bool `pulumi:"arm_boost,optional"`
	DisableOverscan     *bool `pulumi:"disable_overscan,optional"`
	DisableFwKmsSetup   *bool `pulumi:"disable_fw_kms_setup,optional"`
	DisableSplash       *bool `pulumi:"disable_splash,optional"`
	AutoInitramfs       *bool `pulumi:"auto_initramfs,optional"`
	CameraAutoDetect    *bool `pulumi:"camera_auto_detect,optional"`
	DisplayAutoDetect   *bool `pulumi:"display_auto_detect,optional"`
	HdmiForceHotplug    *bool `pulumi:"hdmi_force_hotplug,optional"`
	OtgMode             *bool `pulumi:"otg_mode,optional"`
	UsbMaxCurrentEnable *bool `pulumi:"usb_max_current_enable,optional"`

	HdmiGroup       *float64 `pulumi:"hdmi_group,optional"`
	HdmiDrive       *float64 `pulumi:"hdmi_drive,optional"`
	DisplayRotate   *float64 `pulumi:"display_rotate,optional"`
	MaxFramebuffers *float64 `pulumi:"max_framebuffers,optional"`
	GpuMem          *float64 `pulumi:"gpu_mem,optional"`
	HdmiMode        *float64 `pulumi:"hdmi_mode,optional"`
	SdramFreq       *float64 `pulumi:"sdram_freq,optional"`

	Audio *string `pulumi:"audio,optional"`
}

// BootConfigArgs are the inputs of a BootConfig.
type BootConfigArgs struct {
	Section   *string           `pulumi:"section,optional"`
	Settings  *BootSettings     `pulumi:"settings,optional"`
	Unchecked map[string]string `pulumi:"unchecked,optional"`
	Overlays  []string          `pulumi:"overlays,optional"`
	Path      *string           `pulumi:"path,optional"`
}

// BootConfigState is what is kept in state.
type BootConfigState struct {
	Section    string            `pulumi:"section"`
	Settings   map[string]string `pulumi:"settings"`
	Overlays   []string          `pulumi:"overlays"`
	Path       string            `pulumi:"path"`
	Effective  map[string]string `pulumi:"effective"`
	Overridden []string          `pulumi:"overridden"`
	Unchecked  map[string]string `pulumi:"unchecked,optional"`
}

func (a *BootConfigArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Section, "The filter section the lines go in, all by default.")
	an.Describe(&a.Settings, "Typed settings; booleans are written as 1 and 0.")
	an.Describe(&a.Unchecked, "Any other key=value, written as given. One naming a typed key wins.")
	an.Describe(&a.Overlays, "Whole lines, such as dtoverlay=dwc2,dr_mode=host, kept as they are; overlay names are checked against the machine.")
	an.Describe(&a.Path, "config.txt, when it is not /boot/firmware/config.txt or /boot/config.txt.")
}

func (s *BootConfigState) Annotate(an infer.Annotator) {
	an.Describe(&s.Effective, "What vcgencmd get_config int says the firmware parsed.")
	an.Describe(&s.Overridden, "Settings the firmware resolved differently.")
}

func (r *BootConfig) Annotate(a infer.Annotator) {
	a.Describe(&r, "Lines in one section of a Raspberry Pi's config.txt. Takes effect at the next boot.")
	a.SetToken("index", "BootConfig")
}

var bootCandidates = []string{"/boot/firmware/config.txt", "/boot/config.txt"}

// overlayNameOf is the overlay a dtoverlay line loads, or "" for any other line.
func overlayNameOf(line string) string {
	text := jscompat.Trim(line)
	value, ok := strings.CutPrefix(text, "dtoverlay=")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(value, ",")
	return jscompat.Trim(name)
}

// unknownOverlays is the overlay names the lines load that the machine does not have.
func unknownOverlays(lines, available []string) []string {
	out := []string{}
	for _, line := range lines {
		if name := overlayNameOf(line); name != "" && !slices.Contains(available, name) {
			out = append(out, name)
		}
	}
	return out
}

func bootRendered(a BootConfigArgs) ([]bootconfig.Setting, map[string]string) {
	unchecked := a.Unchecked
	if unchecked == nil {
		unchecked = map[string]string{}
	}
	ordered := bootconfig.RenderInput(presentFields(a.Settings), unchecked)
	settings := map[string]string{}
	for _, s := range ordered {
		settings[s.Key] = s.Value
	}
	return ordered, settings
}

func bootReadCommand(path string) string {
	candidates := bootCandidates
	if path != "" {
		candidates = []string{path}
	}
	tests := make([]string, len(candidates))
	for i, c := range candidates {
		q := shellQuote(c)
		tests[i] = "test -f " + q + " && { echo " + q + "; cat " + q + "; exit 0; }"
	}
	return "{ " + strings.Join(tests, "; ") + "; exit 9; }"
}

type bootRead struct {
	path      string
	lines     []string
	effective map[string]string
}

func readBootConfig(ctx context.Context, m *machine, section, path string) (*bootRead, error) {
	asked, err := m.ask(ctx, m.escalate(bootReadCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read the boot configuration: %s", jscompat.Trim(asked.err))
	}
	head, text := firstLineSplit(asked.out)
	vc, err := m.ask(ctx, m.escalate("vcgencmd get_config int 2>/dev/null || true"))
	if err != nil {
		return nil, err
	}
	effective := map[string]string{}
	if vc.code == 0 {
		effective = bootconfig.ParseVcgencmd(vc.out)
	}
	return &bootRead{path: jscompat.Trim(head), lines: bootconfig.ParseBootSections(text)[section], effective: effective}, nil
}

func readOverlays(ctx context.Context, m *machine, path string) ([]string, error) {
	asked, err := m.ask(ctx, m.escalate("ls -1 "+shellQuote(lastSegment.ReplaceAllString(path, "")+"/overlays")+" 2>/dev/null || true"))
	if err != nil || asked.code != 0 {
		return []string{}, err
	}
	out := []string{}
	for _, line := range strings.Split(asked.out, "\n") {
		if name, ok := strings.CutSuffix(jscompat.Trim(line), ".dtbo"); ok {
			out = append(out, name)
		}
	}
	slices.SortFunc(out, jscompat.CompareUTF16)
	return out, nil
}

func settleBoot(ctx context.Context, m *machine, a BootConfigArgs) (BootConfigState, error) {
	section := orDefault(a.Section, bootconfig.DefaultSection)
	ordered, settings := bootRendered(a)
	overlays := a.Overlays
	if overlays == nil {
		overlays = []string{}
	}
	before, err := readBootConfig(ctx, m, section, deref(a.Path))
	if err != nil {
		return BootConfigState{}, err
	}
	if before == nil {
		looked := strings.Join(bootCandidates, " or ")
		if a.Path != nil {
			looked = *a.Path
		}
		return BootConfigState{}, fmt.Errorf("no boot configuration at %s on %s: this is not an image that boots that way", looked, m.describe())
	}
	available, err := readOverlays(ctx, m, before.path)
	if err != nil {
		return BootConfigState{}, err
	}
	if len(available) > 0 {
		if unknown := unknownOverlays(overlays, available); len(unknown) > 0 {
			return BootConfigState{}, fmt.Errorf("%s has no overlay named %s. "+
				"The overlays it does have are in %s/overlays; "+
				"`dtoverlay -h <name>` documents one. A name that does not exist is not an error at boot — "+
				"the line is simply ignored and the hardware it would have enabled is missing.", m.describe(), strings.Join(unknown, ", "), lastSegment.ReplaceAllString(before.path, ""))
		}
	}
	current, err := m.must(ctx, m.escalate("cat "+shellQuote(before.path)))
	if err != nil {
		return BootConfigState{}, err
	}
	if updated := bootconfig.ApplyToSection(current, section, ordered, overlays); updated != current {
		if _, err := m.must(ctx, m.escalate(heredoc(before.path, updated))); err != nil {
			return BootConfigState{}, err
		}
	}
	actual, err := readBootConfig(ctx, m, section, before.path)
	if err != nil {
		return BootConfigState{}, err
	}
	if actual == nil {
		return BootConfigState{}, fmt.Errorf("wrote %s but it is no longer there", before.path)
	}
	return BootConfigState{Section: section, Settings: settings, Overlays: overlays, Path: actual.path,
		Effective: actual.effective, Unchecked: a.Unchecked, Overridden: resolved.Disagreeing(settings, actual.effective, nil)}, nil
}

// bootFromSection is a section's lines as inputs, for an import: a key set on one line only is a
// setting, every other line that sets something (overlays, dtparams, repeated keys) is kept whole.
func bootFromSection(lines []string) (map[string]string, []string) {
	count := map[string]int{}
	for _, line := range lines {
		if key, ok := bootconfig.KeyOf(line); ok {
			count[key]++
		}
	}
	settings, overlays := map[string]string{}, []string{}
	for _, line := range lines {
		key, ok := bootconfig.KeyOf(line)
		if !ok {
			continue
		}
		text := jscompat.Trim(line)
		if value, plain := strings.CutPrefix(text, key+"="); plain && count[key] == 1 && key != "dtoverlay" && key != "dtparam" {
			settings[key] = value
		} else {
			overlays = append(overlays, text)
		}
	}
	return settings, overlays
}

// Create writes the lines; the id is `<path>#<section>`.
func (BootConfig) Create(ctx context.Context, req infer.CreateRequest[BootConfigArgs]) (infer.CreateResponse[BootConfigState], error) {
	if req.DryRun {
		_, settings := bootRendered(req.Inputs)
		s := BootConfigState{Section: orDefault(req.Inputs.Section, bootconfig.DefaultSection), Settings: settings, Overlays: req.Inputs.Overlays,
			Path: deref(req.Inputs.Path), Unchecked: req.Inputs.Unchecked, Effective: map[string]string{}, Overridden: []string{}}
		if s.Overlays == nil {
			s.Overlays = []string{}
		}
		return infer.CreateResponse[BootConfigState]{ID: s.Path + "#" + s.Section, Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[BootConfigState]{}, err
	}
	state, err := settleBoot(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[BootConfigState]{}, err
	}
	return infer.CreateResponse[BootConfigState]{ID: state.Path + "#" + state.Section, Output: state}, nil
}

// Read reports what the firmware parsed; the file gone, it is forgotten.
func (BootConfig) Read(ctx context.Context, req infer.ReadRequest[BootConfigArgs, BootConfigState]) (infer.ReadResponse[BootConfigArgs, BootConfigState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[BootConfigArgs, BootConfigState]{}, err
	}
	state := req.State
	imported := state.Section == ""
	idPath, idSection := splitOnce(req.ID, "#")
	if imported {
		state.Section, state.Path = idSection, idPath
		if state.Section == "" {
			state.Section = bootconfig.DefaultSection
		}
	}
	actual, err := readBootConfig(ctx, m, state.Section, state.Path)
	if err != nil || actual == nil {
		return infer.ReadResponse[BootConfigArgs, BootConfigState]{}, err
	}
	inputs := req.Inputs
	if imported {
		state.Settings, state.Overlays = bootFromSection(actual.lines)
		inputs = BootConfigArgs{Section: &state.Section, Unchecked: state.Settings, Overlays: state.Overlays, Path: &actual.path}
	}
	if state.Settings == nil {
		state.Settings = map[string]string{}
	}
	if state.Overlays == nil {
		state.Overlays = []string{}
	}
	state.Path, state.Effective = actual.path, actual.effective
	state.Overridden = resolved.Disagreeing(state.Settings, actual.effective, nil)
	state.Unchecked = inputs.Unchecked
	return infer.ReadResponse[BootConfigArgs, BootConfigState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update writes the lines again.
func (BootConfig) Update(ctx context.Context, req infer.UpdateRequest[BootConfigArgs, BootConfigState]) (infer.UpdateResponse[BootConfigState], error) {
	if req.DryRun {
		s := req.State
		_, s.Settings = bootRendered(req.Inputs)
		s.Unchecked = req.Inputs.Unchecked
		s.Overlays = req.Inputs.Overlays
		if s.Overlays == nil {
			s.Overlays = []string{}
		}
		return infer.UpdateResponse[BootConfigState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[BootConfigState]{}, err
	}
	state, err := settleBoot(ctx, m, req.Inputs)
	return infer.UpdateResponse[BootConfigState]{Output: state}, err
}

// Diff: the section, the rendered settings and the overlay lines; another section replaces.
func (BootConfig) Diff(_ context.Context, req infer.DiffRequest[BootConfigArgs, BootConfigState]) (p.DiffResponse, error) {
	old := req.State
	section := orDefault(req.Inputs.Section, bootconfig.DefaultSection)
	_, settings := bootRendered(req.Inputs)
	oldSettings := old.Settings
	if oldSettings == nil {
		oldSettings = map[string]string{}
	}
	changed := old.Section != section || !same.SameJSON(oldSettings, settings) ||
		strings.Join(old.Overlays, "\n") != strings.Join(req.Inputs.Overlays, "\n")
	var replaces []string
	if old.Section != section {
		replaces = []string{"section"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete removes the declared keys and overlay lines from the section.
func (BootConfig) Delete(ctx context.Context, req infer.DeleteRequest[BootConfigState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	path, _ := splitOnce(req.ID, "#")
	current, err := m.must(ctx, m.escalate("cat "+shellQuote(path)))
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	settings := make([]bootconfig.Setting, 0, len(req.State.Settings))
	for _, k := range jscompat.SortedKeys(req.State.Settings) {
		settings = append(settings, bootconfig.Setting{Key: k, Value: req.State.Settings[k]})
	}
	without := bootconfig.RemoveFromBootSection(current, req.State.Section, settings, req.State.Overlays)
	_, err = m.must(ctx, m.escalate(heredoc(path, without)))
	return infer.DeleteResponse{}, err
}
