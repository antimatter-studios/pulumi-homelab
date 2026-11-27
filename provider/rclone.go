package homelab

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/rclone"
	"github.com/antimatter-studios/pulumi-homelab/provider/same"
)

// RcloneRemote is a named backend in rclone's config file. Ported from src/resources/rclone.ts.
//
// rclone stores passwords obscured, and `rclone obscure` is not deterministic, so comparing a
// stored value against a freshly obscured one would report drift for ever. The comparison happens
// in plaintext instead: `rclone reveal` turns what is stored back into what it was. The plaintext
// is therefore in the state, marked secret.
type RcloneRemote struct{}

// RcloneRemoteArgs are the inputs of an RcloneRemote.
type RcloneRemoteArgs struct {
	Remote   string            `pulumi:"remote"`
	Type     string            `pulumi:"type"`
	Settings map[string]string `pulumi:"settings,optional"`
	Secrets  map[string]string `pulumi:"secrets,optional" provider:"secret"`
	Config   *string           `pulumi:"config,optional"`
}

// RcloneRemoteState is what is kept in state.
type RcloneRemoteState struct {
	Remote   string            `pulumi:"remote"`
	Type     string            `pulumi:"type"`
	Settings map[string]string `pulumi:"settings"`
	Secrets  map[string]string `pulumi:"secrets" provider:"secret"`
	Config   string            `pulumi:"config"`
}

func (a *RcloneRemoteArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Remote, "The remote's name, as it appears in [brackets] and before the colon in a path.")
	an.Describe(&a.Type, "The backend: sftp, s3, webdav, and so on.")
	an.Describe(&a.Settings, "Everything that is not a credential, keyed as rclone spells it.")
	an.Describe(&a.Secrets, "Credentials, given in plaintext and stored obscured. Read back revealed, so the state holds them in plaintext, marked secret.")
	an.Describe(&a.Config, "Which config file to write. Defaults to root's, /root/.config/rclone/rclone.conf.")
}

func (r *RcloneRemote) Annotate(an infer.Annotator) {
	an.Describe(&r, "A remote rclone should know about, read back in plaintext so drift is detectable at all.")
	an.SetToken("index", "RcloneRemote")
}

const rcloneDefaultConfig = "/root/.config/rclone/rclone.conf"

// configPairs are the key value pairs for `rclone config create`, sorted so a rearranged settings
// map is not a different command.
func configPairs(settings map[string]string) []string {
	pairs := []string{}
	for _, key := range slices.SortedFunc(maps.Keys(settings), jscompat.CompareUTF16) {
		pairs = append(pairs, key, settings[key])
	}
	return pairs
}

type storedRemote struct {
	Type              string
	Settings, Secrets map[string]string
	SettingKeys       []string
}

// readRemote is what is actually stored, with the credentials turned back into what they were, or
// nil when there is no such remote.
func readRemote(ctx context.Context, m *machine, remote string, secretKeys []string, config string) (*storedRemote, error) {
	dumped, err := m.ask(ctx, m.escalate("rclone --config "+shellQuote(config)+" config dump 2>/dev/null"))
	if err != nil || dumped.code != 0 {
		return nil, err
	}
	stored, keys := rclone.ParseDumpOrdered(dumped.out, remote)
	if stored == nil {
		return nil, nil
	}
	read := &storedRemote{Type: stored["type"], Settings: map[string]string{}, Secrets: map[string]string{}}
	for _, key := range secretKeys {
		obscured, ok := stored[key]
		if !ok {
			continue
		}
		// reveal rather than obscure-and-compare: obscuring is not deterministic
		revealed, err := m.ask(ctx, m.escalate("rclone reveal "+shellQuote(obscured)+" 2>/dev/null"))
		if err != nil {
			return nil, err
		}
		if revealed.code == 0 {
			read.Secrets[key] = strings.TrimSuffix(revealed.out, "\n")
		}
	}
	for _, key := range keys {
		if key == "type" || slices.Contains(secretKeys, key) {
			continue
		}
		read.Settings[key] = stored[key]
		read.SettingKeys = append(read.SettingKeys, key)
	}
	return read, nil
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

var lastPathPart = regexp.MustCompile(`/[^/]*$`)

// rcloneCreateCommand writes the remote. --obscure asks rclone to obscure the values its own
// backend schema says are passwords, rather than this code guessing which they are.
func rcloneCreateCommand(config, remote, typ string, settings, secrets map[string]string) string {
	quoted := []string{}
	for _, v := range append(configPairs(settings), configPairs(secrets)...) {
		quoted = append(quoted, shellQuote(v))
	}
	dir := lastPathPart.ReplaceAllString(config, "")
	if dir == "" {
		dir = "/"
	}
	return "mkdir -p " + shellQuote(dir) + " && " +
		"rclone --config " + shellQuote(config) + " config create " + shellQuote(remote) + " " + shellQuote(typ) + " " + strings.Join(quoted, " ") + " " +
		"--obscure --non-interactive >/dev/null"
}

func settleRemote(ctx context.Context, m *machine, args RcloneRemoteArgs) (RcloneRemoteState, error) {
	config := orDefault(args.Config, rcloneDefaultConfig)
	settings, secrets := nonNil(args.Settings), nonNil(args.Secrets)
	secretKeys := jscompat.SortedKeys(secrets)

	current, err := readRemote(ctx, m, args.Remote, secretKeys, config)
	if err != nil {
		return RcloneRemoteState{}, err
	}
	unchanged := current != nil && current.Type == args.Type &&
		slices.Equal(current.SettingKeys, jscompat.SortedKeys(settings)) && maps.Equal(current.Settings, settings)
	for _, key := range secretKeys {
		if unchanged {
			got, ok := current.Secrets[key]
			unchanged = ok && got == secrets[key]
		}
	}
	// an update that changes nothing does nothing: rewriting a config file to the same values is
	// churn on a file holding passwords
	if !unchanged {
		if _, err := m.must(ctx, m.escalate(rcloneCreateCommand(config, args.Remote, args.Type, settings, secrets))); err != nil {
			return RcloneRemoteState{}, err
		}
	}
	actual, err := readRemote(ctx, m, args.Remote, secretKeys, config)
	if err != nil {
		return RcloneRemoteState{}, err
	}
	if actual == nil {
		return RcloneRemoteState{}, fmt.Errorf("wrote the remote %s but rclone does not report it", args.Remote)
	}
	return RcloneRemoteState{Remote: args.Remote, Type: actual.Type, Settings: actual.Settings, Secrets: secrets, Config: config}, nil
}

// Create writes the remote, unless it is already there as declared.
func (RcloneRemote) Create(ctx context.Context, req infer.CreateRequest[RcloneRemoteArgs]) (infer.CreateResponse[RcloneRemoteState], error) {
	args := req.Inputs
	if req.DryRun {
		return infer.CreateResponse[RcloneRemoteState]{ID: args.Remote, Output: RcloneRemoteState{
			Remote: args.Remote, Type: args.Type, Settings: nonNil(args.Settings), Secrets: nonNil(args.Secrets),
			Config: orDefault(args.Config, rcloneDefaultConfig),
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[RcloneRemoteState]{}, err
	}
	state, err := settleRemote(ctx, m, args)
	if err != nil {
		return infer.CreateResponse[RcloneRemoteState]{}, err
	}
	return infer.CreateResponse[RcloneRemoteState]{ID: args.Remote, Output: state}, nil
}

// Read: gone from the config and Pulumi forgets it, so the next up puts it back. The machine's
// answer wins, with the secrets revealed so the comparison happens in plaintext at both ends.
func (RcloneRemote) Read(ctx context.Context, req infer.ReadRequest[RcloneRemoteArgs, RcloneRemoteState]) (infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState]{}, err
	}
	state := req.State
	if state.Config == "" {
		state.Config = orDefault(req.Inputs.Config, rcloneDefaultConfig)
		if raw, ok := infer.GetRawRequest[p.ReadRequest](ctx); ok && raw.Properties.Get("config").IsString() {
			state.Config = raw.Properties.Get("config").AsString()
		}
	}
	if state.Secrets == nil {
		state.Secrets = req.Inputs.Secrets
	}
	actual, err := readRemote(ctx, m, req.ID, jscompat.SortedKeys(state.Secrets), state.Config)
	if err != nil || actual == nil {
		return infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState]{}, err
	}
	secrets := maps.Clone(nonNil(state.Secrets))
	maps.Copy(secrets, actual.Secrets)
	state.Remote, state.Type, state.Settings, state.Secrets = req.ID, actual.Type, actual.Settings, secrets
	inputs := req.Inputs
	if inputs.Remote == "" {
		config := state.Config
		inputs = RcloneRemoteArgs{Remote: req.ID, Type: actual.Type, Settings: actual.Settings, Secrets: secrets, Config: &config}
	}
	return infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update writes the remote again, under its id.
func (RcloneRemote) Update(ctx context.Context, req infer.UpdateRequest[RcloneRemoteArgs, RcloneRemoteState]) (infer.UpdateResponse[RcloneRemoteState], error) {
	args := req.Inputs
	args.Remote = req.ID
	if req.DryRun {
		return infer.UpdateResponse[RcloneRemoteState]{Output: RcloneRemoteState{
			Remote: args.Remote, Type: args.Type, Settings: nonNil(args.Settings), Secrets: nonNil(args.Secrets),
			Config: orDefault(args.Config, rcloneDefaultConfig),
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[RcloneRemoteState]{}, err
	}
	state, err := settleRemote(ctx, m, args)
	if err != nil {
		return infer.UpdateResponse[RcloneRemoteState]{}, err
	}
	return infer.UpdateResponse[RcloneRemoteState]{Output: state}, nil
}

// Diff compares settings and secrets in plaintext on both sides. A different remote name replaces,
// deleting first.
func (RcloneRemote) Diff(_ context.Context, req infer.DiffRequest[RcloneRemoteArgs, RcloneRemoteState]) (p.DiffResponse, error) {
	old, args := req.State, req.Inputs
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	changed := func(key string, yes bool) {
		if yes {
			resp.DetailedDiff[key] = p.PropertyDiff{Kind: p.Update}
		}
	}
	changed("type", old.Type != args.Type)
	changed("config", old.Config != orDefault(args.Config, rcloneDefaultConfig))
	changed("settings", !same.SameJSON(nonNil(old.Settings), nonNil(args.Settings)))
	changed("secrets", !same.SameJSON(nonNil(old.Secrets), nonNil(args.Secrets)))
	if old.Remote != args.Remote {
		resp.DetailedDiff["remote"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete removes the remote from the config it was written to; nothing it pointed at is touched,
// and nothing mounted from it is unmounted.
func (RcloneRemote) Delete(ctx context.Context, req infer.DeleteRequest[RcloneRemoteState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	config := req.State.Config
	if config == "" {
		if raw, ok := infer.GetRawRequest[p.DeleteRequest](ctx); !ok || !raw.Properties.Get("config").IsString() {
			config = rcloneDefaultConfig
		}
	}
	_, err = m.must(ctx, m.escalate("rclone --config "+shellQuote(config)+" config delete "+shellQuote(req.ID)+" 2>/dev/null || true"))
	return infer.DeleteResponse{}, err
}
