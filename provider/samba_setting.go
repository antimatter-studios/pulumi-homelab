package homelab

import (
	"context"
	"fmt"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/samba"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// SambaSetting is one parameter in one section of smb.conf, edited in place. Ported from
// src/resources/samba.ts SambaSetting. Its id is share#key.
type SambaSetting struct{}

// SambaSettingArgs are the inputs of a SambaSetting.
type SambaSettingArgs struct {
	Share  string  `pulumi:"share"`
	Key    string  `pulumi:"key"`
	Value  string  `pulumi:"value"`
	Apply  *string `pulumi:"apply,optional"`
	Config *string `pulumi:"config,optional"`
}

// SambaSettingState is what is kept in state.
type SambaSettingState struct {
	Share     string `pulumi:"share"`
	Key       string `pulumi:"key"`
	Value     string `pulumi:"value"`
	Apply     string `pulumi:"apply"`
	Config    string `pulumi:"config"`
	Actual    string `pulumi:"actual"`
	Effective string `pulumi:"effective"`
}

func (s *SambaSettingArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Share, "The section, as in global.")
	a.Describe(&s.Key, "The parameter name.")
	a.Describe(&s.Value, "Its value.")
	a.Describe(&s.Apply, "How a change takes effect: reload (smbcontrol reload-config), nmbd (also restart nmbd) or all (restart nmbd and smbd).")
	a.SetDefault(&s.Apply, "reload")
	a.Describe(&s.Config, "The smb.conf to edit.")
	a.SetDefault(&s.Config, smbConf)
}

func (s *SambaSettingState) Annotate(a infer.Annotator) {
	a.Describe(&s.Actual, "The value as the file says it.")
	a.Describe(&s.Effective, "The value as testparm -sv reports it, defaults included.")
}

func (s *SambaSetting) Annotate(a infer.Annotator) {
	a.Describe(&s, "One samba parameter, written into its section of smb.conf.")
	a.SetToken("index", "SambaSetting")
}

var sambaApply = map[string]string{
	"reload": "smbcontrol all reload-config >/dev/null 2>&1 || true",
	"nmbd":   "smbcontrol all reload-config >/dev/null 2>&1; systemctl try-restart nmbd 2>/dev/null || true",
	"all":    "systemctl try-restart nmbd 2>/dev/null; systemctl try-restart smbd 2>/dev/null || true",
}

// splitSettingID is const [share = ”, key = ”] = id.split('#').
func splitSettingID(id string) (string, string) {
	parts := strings.Split(id, "#")
	key := ""
	if len(parts) > 1 {
		key = parts[1]
	}
	return parts[0], key
}

func readSetting(ctx context.Context, m *machine, share, key, config string) (string, bool, error) {
	effective, ok, err := readShare(ctx, m, share, config)
	if err != nil || !ok {
		return "", false, err
	}
	value, found := effective.Find(key)
	return value, found, nil
}

func orApply(apply *string) string {
	if apply == nil {
		return "reload"
	}
	return *apply
}

func settleSetting(ctx context.Context, m *machine, args SambaSettingArgs) (SambaSettingState, error) {
	config, how := orConf(args.Config), orApply(args.Apply)
	current, err := catConf(ctx, m, config)
	if err != nil {
		return SambaSettingState{}, err
	}
	updated := samba.UpsertSetting(current, args.Share, args.Key, args.Value)
	if updated != current {
		if err := applyConf(ctx, m, updated, config); err != nil {
			return SambaSettingState{}, err
		}
		command, ok := sambaApply[how]
		if !ok {
			command = sambaApply["reload"]
		}
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return SambaSettingState{}, err
		}
	}
	written, err := catConf(ctx, m, config)
	if err != nil {
		return SambaSettingState{}, err
	}
	actual, found := samba.FileSetting(written, args.Share, args.Key)
	if !found || !sambaSameValue(args.Key, args.Value, actual) {
		shown := actual
		if !found {
			shown = "nothing"
		}
		return SambaSettingState{}, fmt.Errorf("wrote %s to [%s] in %s on %s and it reads back as "+
			"%s — something else is editing the same file", args.Key, args.Share, config, m.describe(), shown)
	}
	effective, _, err := readSetting(ctx, m, args.Share, args.Key, config)
	if err != nil {
		return SambaSettingState{}, err
	}
	return SambaSettingState{Share: args.Share, Key: args.Key, Value: args.Value, Apply: how, Config: config, Actual: actual, Effective: effective}, nil
}

func previewSetting(args SambaSettingArgs) SambaSettingState {
	return SambaSettingState{Share: args.Share, Key: args.Key, Value: args.Value, Apply: orApply(args.Apply), Config: orConf(args.Config)}
}

// Create writes the setting, or adopts one the file already has.
func (SambaSetting) Create(ctx context.Context, req infer.CreateRequest[SambaSettingArgs]) (infer.CreateResponse[SambaSettingState], error) {
	id := req.Inputs.Share + "#" + req.Inputs.Key
	if req.DryRun {
		return infer.CreateResponse[SambaSettingState]{ID: id, Output: previewSetting(req.Inputs)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SambaSettingState]{}, err
	}
	state, err := settleSetting(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[SambaSettingState]{}, err
	}
	return infer.CreateResponse[SambaSettingState]{ID: id, Output: state}, nil
}

// Read reports the value as the file says it, and as testparm resolves it; a setting gone from the
// file is forgotten. On import the inputs are the value the file has.
func (SambaSetting) Read(ctx context.Context, req infer.ReadRequest[SambaSettingArgs, SambaSettingState]) (infer.ReadResponse[SambaSettingArgs, SambaSettingState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SambaSettingArgs, SambaSettingState]{}, err
	}
	share, key := splitSettingID(req.ID)
	config := req.State.Config
	if config == "" {
		config = orConf(req.Inputs.Config)
	}
	text, err := m.ask(ctx, m.escalate("cat "+shellQuote(config)))
	if err != nil {
		return infer.ReadResponse[SambaSettingArgs, SambaSettingState]{}, err
	}
	actual, found := "", false
	if text.code == 0 {
		actual, found = samba.FileSetting(text.out, share, key)
	}
	if !found {
		return infer.ReadResponse[SambaSettingArgs, SambaSettingState]{}, nil
	}
	effective, _, err := readSetting(ctx, m, share, key, config)
	if err != nil {
		return infer.ReadResponse[SambaSettingArgs, SambaSettingState]{}, err
	}
	state := req.State
	inputs := req.Inputs
	if state.Share == "" {
		value := actual
		if inputs.Share != "" || inputs.Key != "" {
			value = inputs.Value
		} else {
			inputs.Value = value
		}
		state = SambaSettingState{Value: value, Apply: orApply(inputs.Apply)}
		inputs.Share, inputs.Key = share, key
		if req.Inputs.Share == "" && req.Inputs.Key == "" {
			inputs.Apply, inputs.Config = &state.Apply, &config
		}
	}
	if state.Apply == "" {
		state.Apply = "reload"
	}
	state.Config, state.Share, state.Key, state.Actual, state.Effective = config, share, key, actual, effective
	return infer.ReadResponse[SambaSettingArgs, SambaSettingState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update writes the setting under its id.
func (SambaSetting) Update(ctx context.Context, req infer.UpdateRequest[SambaSettingArgs, SambaSettingState]) (infer.UpdateResponse[SambaSettingState], error) {
	args := req.Inputs
	args.Share, args.Key = splitSettingID(req.ID)
	if req.DryRun {
		return infer.UpdateResponse[SambaSettingState]{Output: previewSetting(args)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SambaSettingState]{}, err
	}
	state, err := settleSetting(ctx, m, args)
	if err != nil {
		return infer.UpdateResponse[SambaSettingState]{}, err
	}
	return infer.UpdateResponse[SambaSettingState]{Output: state}, nil
}

// Diff: the file disagreeing, a new declared value or a new apply is an update; a new share or key
// replaces, deleting the old line first.
func (SambaSetting) Diff(_ context.Context, req infer.DiffRequest[SambaSettingArgs, SambaSettingState]) (p.DiffResponse, error) {
	old, args := req.State, req.Inputs
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if !sambaSameValue(args.Key, args.Value, old.Actual) || old.Value != args.Value {
		resp.DetailedDiff["value"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Apply != orApply(args.Apply) {
		resp.DetailedDiff["apply"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Share != args.Share || old.Key != args.Key {
		resp.DetailedDiff["share"] = p.PropertyDiff{Kind: p.UpdateReplace}
		resp.DetailedDiff["key"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete removes this setting's own line from its section and nothing else.
func (SambaSetting) Delete(ctx context.Context, req infer.DeleteRequest[SambaSettingState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	share, key := splitSettingID(req.ID)
	config := req.State.Config
	if config == "" {
		config = smbConf
	}
	current, err := catConf(ctx, m, config)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	return infer.DeleteResponse{}, applyConf(ctx, m, samba.RemoveSetting(current, share, key), config)
}
