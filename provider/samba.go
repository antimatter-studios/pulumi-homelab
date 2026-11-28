package homelab

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/samba"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// The shared smb.conf machinery of SambaShare, SambaSetting and SambaUser, ported from
// src/resources/samba.ts. The pure file editing is provider/samba.

const smbConf = "/etc/samba/smb.conf"

// Hold across the read, edit, install and readback in both resources. Different
// provider instances in this process can reach the same file via different SSH routes.
var sambaConfigMu sync.Mutex

var (
	sambaYes        = []string{"yes", "true", "1"}
	sambaNo         = []string{"no", "false", "0"}
	sambaCaseFolded = []string{"netbios name", "workgroup", "server string", "realm"}
)

// sambaSameValue says whether a declared value and the one samba reports mean the same: whitespace
// around either is ignored, every boolean spelling samba accepts is one value, and the names samba
// uppercases are compared case-folded. Anything else, a path included, is compared exactly.
func sambaSameValue(key, declared, effective string) bool {
	a, b := jscompat.Trim(declared), jscompat.Trim(effective)
	if a == b {
		return true
	}
	lowerA, lowerB := jscompat.ToLower(a), jscompat.ToLower(b)
	if slices.Contains(sambaYes, lowerA) && slices.Contains(sambaYes, lowerB) {
		return true
	}
	if slices.Contains(sambaNo, lowerA) && slices.Contains(sambaNo, lowerB) {
		return true
	}
	folded := slices.ContainsFunc(sambaCaseFolded, func(name string) bool { return samba.SameKey(name, key) })
	return folded && lowerA == lowerB
}

// shareDiffers says whether any wanted setting is missing from actual or means something else.
func shareDiffers(actual, wanted samba.Settings) bool {
	for _, w := range wanted.Entries() {
		found, ok := actual.Find(w.Key)
		if !ok || !sambaSameValue(w.Key, w.Value, found) {
			return true
		}
	}
	return false
}

// testparmCommand asks testparm for the effective configuration, defaults included (-v), without
// its prompt (-s), and without its commentary.
func testparmCommand(config string) string {
	return "testparm -sv " + shellQuote(config) + " 2>/dev/null"
}

// narrowTo keeps only the asked-about keys of testparm's answer, under the name samba reports.
func narrowTo(effective samba.Settings, keys []string) samba.Settings {
	kept := samba.Settings{}
	for _, key := range keys {
		for _, e := range effective.Entries() {
			if samba.SameKey(e.Key, key) {
				kept = append(kept, e)
				break
			}
		}
	}
	return kept
}

// sambaApplyCommand stages the new file, refuses one testparm cannot parse, installs it and asks
// samba to reload.
func sambaApplyCommand(updated, config string) string {
	return `staging=$(mktemp) && trap 'rm -f "$staging"' EXIT && ` +
		heredocInto(`"$staging"`, updated,
			`{ testparm -s "$staging" >/dev/null 2>&1 || { echo "the resulting smb.conf does not parse" >&2; exit 1; }; }`,
			`install -m 0644 -o root -g root "$staging" `+shellQuote(config),
			// There may be no running Samba process to notify.
			`{ smbcontrol all reload-config >/dev/null 2>&1 || true; }`)
}

func catConf(ctx context.Context, m *machine, config string) (string, error) {
	return m.must(ctx, m.escalate("cat "+shellQuote(config)))
}

func applyConf(ctx context.Context, m *machine, updated, config string) error {
	_, err := m.must(ctx, m.escalate(sambaApplyCommand(updated, config)))
	return err
}

// readShare is the effective section as testparm -sv reports it, or nil when samba has no such
// section.
func readShare(ctx context.Context, m *machine, share, config string) (samba.Settings, bool, error) {
	asked, err := m.ask(ctx, m.escalate(testparmCommand(config)))
	if err != nil {
		return nil, false, err
	}
	if asked.code != 0 {
		return nil, false, fmt.Errorf("samba's own configuration does not parse: %s", jscompat.Trim(asked.err))
	}
	settings, ok := samba.EffectiveShare(asked.out, share)
	return settings, ok, nil
}

func readShareSectionCommand(config string) string {
	q := shellQuote(config)
	return "test -f " + q + " || exit 9; cat " + q + " && echo '#pulumi-homelab#parses' && { " +
		"testparm -s " + q + " >/dev/null 2>&1 && echo ok || true; }"
}

// readShareSection is the section as the file says it, and whether samba can parse the file; nil
// when there is no file.
func readShareSection(ctx context.Context, m *machine, share, config string) (*samba.ShareRead, error) {
	asked, err := m.ask(ctx, m.escalate(readShareSectionCommand(config)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", config, jscompat.Trim(asked.err))
	}
	read := samba.ParseShareRead(asked.out, share)
	return &read, nil
}

func orConf(config *string) string {
	if config == nil {
		return smbConf
	}
	return *config
}

func settingsMap(s samba.Settings) map[string]string {
	out := s.Map()
	if out == nil {
		out = map[string]string{}
	}
	return out
}

// SambaShare is one [share] section of smb.conf, edited in place. Ported from
// src/resources/samba.ts SambaShare.
type SambaShare struct{}

// SambaShareArgs are the inputs of a SambaShare.
type SambaShareArgs struct {
	Share    string            `pulumi:"share"`
	Path     string            `pulumi:"path"`
	Settings map[string]string `pulumi:"settings,optional"`
	Config   *string           `pulumi:"config,optional"`
}

// SambaShareState is what is kept in state.
type SambaShareState struct {
	Share    string            `pulumi:"share"`
	Path     string            `pulumi:"path"`
	Settings map[string]string `pulumi:"settings"`
	Config   string            `pulumi:"config"`
	Actual   map[string]string `pulumi:"actual"`
	Parses   bool              `pulumi:"parses"`
	Created  *bool             `pulumi:"created,optional"`
}

func (s *SambaShareArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Share, "The section name, as in [photos].")
	a.Describe(&s.Path, "The directory the share serves.")
	a.Describe(&s.Settings, "Further parameters of the section, written as given.")
	a.Describe(&s.Config, "The smb.conf to edit.")
	a.SetDefault(&s.Config, smbConf)
}

func (s *SambaShareState) Annotate(a infer.Annotator) {
	a.Describe(&s.Actual, "The section as the file says it.")
	a.Describe(&s.Parses, "Whether testparm can parse the whole file.")
	a.Describe(&s.Created, "Whether the provider added this section, used to guard deletion.")
}

func (s *SambaShare) Annotate(a infer.Annotator) {
	a.Describe(&s, "A samba share: one section of smb.conf, edited in place and compared against the file.")
	a.SetToken("index", "SambaShare")
}

// wantedShare is { path, ...settings }.
func wantedShare(path string, settings map[string]string) samba.Settings {
	wanted := samba.Settings{{Key: "path", Value: path}}
	for _, s := range samba.SettingsFromInput(settings) {
		if i := slices.IndexFunc(wanted, func(w samba.Setting) bool { return w.Key == s.Key }); i >= 0 {
			wanted[i].Value = s.Value
		} else {
			wanted = append(wanted, s)
		}
	}
	return wanted
}

func settleShare(ctx context.Context, m *machine, args SambaShareArgs, previous *SambaShareState) (SambaShareState, error) {
	config := orConf(args.Config)
	settings := args.Settings
	if settings == nil {
		settings = map[string]string{}
	}
	sambaConfigMu.Lock()
	defer sambaConfigMu.Unlock()

	current, err := catConf(ctx, m, config)
	if err != nil {
		return SambaShareState{}, err
	}
	var created *bool
	if previous == nil {
		added := true
		for name := range samba.ParseSections(current) {
			if strings.EqualFold(name, args.Share) {
				added = false
				break
			}
		}
		created = &added
	} else if previous.Config == config {
		created = previous.Created
	}
	updated := samba.UpsertSection(current, args.Share, samba.ShareSection(args.Share, args.Path, samba.SettingsFromInput(settings)))
	if updated != current {
		if err := applyConf(ctx, m, updated, config); err != nil {
			return SambaShareState{}, err
		}
	}
	after, err := readShareSection(ctx, m, args.Share, config)
	if err != nil {
		return SambaShareState{}, err
	}
	if after == nil || !after.Found {
		return SambaShareState{}, fmt.Errorf("wrote the [%s] section to %s but it is not there", args.Share, config)
	}
	if shareDiffers(after.Actual, wantedShare(args.Path, settings)) {
		return SambaShareState{}, fmt.Errorf("wrote the [%s] section to %s on %s and it does not read "+
			"back as declared — something else is editing the same file", args.Share, config, m.describe())
	}
	return SambaShareState{Share: args.Share, Path: args.Path, Settings: settings, Config: config,
		Actual: settingsMap(after.Actual), Parses: after.Parses, Created: created}, nil
}

func previewShare(args SambaShareArgs) SambaShareState {
	settings := args.Settings
	if settings == nil {
		settings = map[string]string{}
	}
	return SambaShareState{Share: args.Share, Path: args.Path, Settings: settings, Config: orConf(args.Config), Actual: map[string]string{}}
}

// Create writes the section, or adopts one that already says what is declared.
func (SambaShare) Create(ctx context.Context, req infer.CreateRequest[SambaShareArgs]) (infer.CreateResponse[SambaShareState], error) {
	if req.DryRun {
		return infer.CreateResponse[SambaShareState]{ID: req.Inputs.Share, Output: previewShare(req.Inputs)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SambaShareState]{}, err
	}
	state, err := settleShare(ctx, m, req.Inputs, nil)
	if err != nil {
		return infer.CreateResponse[SambaShareState]{}, err
	}
	return infer.CreateResponse[SambaShareState]{ID: req.Inputs.Share, Output: state}, nil
}

// Read reports the section as the file says it; a section or file gone is forgotten. On import the
// inputs are the section as the file has it.
func (SambaShare) Read(ctx context.Context, req infer.ReadRequest[SambaShareArgs, SambaShareState]) (infer.ReadResponse[SambaShareArgs, SambaShareState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SambaShareArgs, SambaShareState]{}, err
	}
	config := req.State.Config
	if config == "" {
		config = orConf(req.Inputs.Config)
	}
	found, err := readShareSection(ctx, m, req.ID, config)
	if err != nil {
		return infer.ReadResponse[SambaShareArgs, SambaShareState]{}, err
	}
	if found == nil || !found.Found {
		return infer.ReadResponse[SambaShareArgs, SambaShareState]{}, nil
	}
	state := req.State
	if state.Settings == nil {
		state.Settings = req.Inputs.Settings
		if state.Settings == nil {
			state.Settings = map[string]string{}
		}
	}
	state.Config, state.Share = config, req.ID
	if path, ok := found.Actual.Get("path"); ok {
		state.Path = path
	}
	state.Actual, state.Parses = settingsMap(found.Actual), found.Parses
	inputs := req.Inputs
	if inputs.Share == "" {
		inputs = SambaShareArgs{Share: req.ID, Path: state.Path, Settings: map[string]string{}, Config: &config}
		for _, s := range found.Actual {
			if s.Key != "path" {
				inputs.Settings[s.Key] = s.Value
			}
		}
		if req.State.Share == "" {
			state.Settings = inputs.Settings
		}
	}
	return infer.ReadResponse[SambaShareArgs, SambaShareState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update rewrites the section under its id.
func (SambaShare) Update(ctx context.Context, req infer.UpdateRequest[SambaShareArgs, SambaShareState]) (infer.UpdateResponse[SambaShareState], error) {
	args := req.Inputs
	args.Share = req.ID
	if req.DryRun {
		return infer.UpdateResponse[SambaShareState]{Output: previewShare(args)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SambaShareState]{}, err
	}
	state, err := settleShare(ctx, m, args, &req.State)
	if err != nil {
		return infer.UpdateResponse[SambaShareState]{}, err
	}
	return infer.UpdateResponse[SambaShareState]{Output: state}, nil
}

// Diff compares the declaration against the file, not against testparm; a file samba cannot parse
// is a change; a new share name replaces, deleting the old section first.
func (SambaShare) Diff(_ context.Context, req infer.DiffRequest[SambaShareArgs, SambaShareState]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if shareDiffers(samba.SettingsFromInput(req.State.Actual), wantedShare(req.Inputs.Path, req.Inputs.Settings)) {
		resp.DetailedDiff["settings"] = p.PropertyDiff{Kind: p.Update}
	}
	if !req.State.Parses {
		resp.DetailedDiff["config"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Share != req.Inputs.Share {
		resp.DetailedDiff["share"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete removes this share's own section and nothing else of the file.
func (SambaShare) Delete(ctx context.Context, req infer.DeleteRequest[SambaShareState]) (infer.DeleteResponse, error) {
	config := req.State.Config
	if config == "" {
		config = smbConf
	}
	if !deref(req.State.Created) {
		warnRetained(ctx, fmt.Sprintf("Samba share %q in %s", req.ID, config), "state does not record that the provider created it.")
		return infer.DeleteResponse{}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	sambaConfigMu.Lock()
	defer sambaConfigMu.Unlock()

	current, err := catConf(ctx, m, config)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	updated := samba.RemoveSection(current, req.ID)
	if updated == current {
		return infer.DeleteResponse{}, nil
	}
	return infer.DeleteResponse{}, applyConf(ctx, m, updated, config)
}
