package homelab

import (
	"context"
	"encoding/json"
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
// in plaintext instead: the provider decodes the stored values using rclone’s format. The plaintext
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

type storedRemote struct {
	Type              string
	Settings, Secrets map[string]string
	SettingKeys       []string
}

func runRcloneConfig(ctx context.Context, m *machine, config, args string) (string, error) {
	command := m.escalate("rclone --config " + shellQuote(config) + " config " + args)
	result, err := m.ask(ctx, command)
	if err != nil {
		// The runner may report partial config output on a connection failure.
		return "", fmt.Errorf("rclone config %s for %q on %s failed: the connection failed", args, config, m.describe())
	}
	if result.code != 0 {
		// Both a partial dump and a config parser error can contain credentials.
		return "", fmt.Errorf("rclone config %s for %q on %s failed (exit %d)", args, config, m.describe(), result.code)
	}
	return result.out, nil
}

func dumpRemote(ctx context.Context, m *machine, remote, config string) (map[string]string, []string, error) {
	dump, err := runRcloneConfig(ctx, m, config, "dump")
	if err != nil {
		return nil, nil, err
	}
	return rclone.ParseDumpChecked(dump, remote)
}

// readRemote is what is actually stored, with the credentials turned back into what they were, or
// nil when there is no such remote.
func readRemote(ctx context.Context, m *machine, remote string, secretKeys []string, config string) (*storedRemote, error) {
	stored, keys, err := dumpRemote(ctx, m, remote, config)
	if err != nil || stored == nil {
		return nil, err
	}
	read := &storedRemote{Type: stored["type"], Settings: map[string]string{}, Secrets: map[string]string{}}
	for _, key := range secretKeys {
		obscured, ok := stored[key]
		if !ok {
			continue
		}
		if revealed, ok := rclone.Reveal(obscured); ok {
			read.Secrets[key] = revealed
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

// The config/create API uses the same backend config and obscuring logic as the CLI.
type rcloneCreateRequest struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Parameters map[string]string `json:"parameters"`
	Opt        struct {
		Obscure        bool `json:"obscure"`
		NonInteractive bool `json:"nonInteractive"`
	} `json:"opt"`
}

func rcloneCreatePayload(remote, typ string, settings, secrets map[string]string) ([]byte, error) {
	var req rcloneCreateRequest
	req.Name, req.Type = remote, typ
	req.Parameters = make(map[string]string, len(settings)+len(secrets))
	maps.Copy(req.Parameters, settings)
	maps.Copy(req.Parameters, secrets)
	req.Opt.Obscure, req.Opt.NonInteractive = true, true
	return json.Marshal(req)
}

// A private, per-call CA authenticates both ends of the loopback connection. The
// command reads the request from stdin and cleans up the daemon and its credentials.
const rcloneCreateViaRC = `command -v openssl >/dev/null 2>&1 || exit 90
command -v curl >/dev/null 2>&1 || exit 91
command -v rclone >/dev/null 2>&1 || exit 92
umask 077
work=$(mktemp -d) || exit 1
pid=
cleanup() {
	if [ -n "$pid" ]; then
		kill "$pid" 2>/dev/null
		wait "$pid" 2>/dev/null
	fi
	rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 1 \
	-subj /CN=127.0.0.1 -addext subjectAltName=IP:127.0.0.1 \
	-keyout "$work/key" -out "$work/cert" >/dev/null 2>&1 || exit 1
unset RCLONE_RC_ADDR
rclone --config CONFIG_FILE rcd --rc-addr 127.0.0.1:0 \
	--rc-cert "$work/cert" --rc-key "$work/key" --rc-client-ca "$work/cert" \
	--rc-no-auth --rc-enable-metrics=false --rc-web-gui=false --rc-files "" --rc-baseurl / \
	--log-level NOTICE --log-file "$work/log" </dev/null >/dev/null 2>&1 &
pid=$!
attempt=0
while kill -0 "$pid" 2>/dev/null; do
	url=$(sed -n 's@.*Serving remote control on \(https://127\.0\.0\.1:[0-9][0-9]*\)/.*@\1@p' "$work/log" 2>/dev/null)
	if [ -n "$url" ]; then
		# Keep any request diagnostics off the filesystem while the daemon is active.
		rm -f -- "$work/log" || exit 1
		curl -qfsS --connect-timeout 5 --max-time 60 --noproxy '*' --cacert "$work/cert" --cert "$work/cert" --key "$work/key" \
			-H 'Content-Type: application/json' --data-binary @- "$url/config/create" >/dev/null || exit 1
		exit 0
	fi
	attempt=$((attempt + 1))
	[ "$attempt" -lt 100 ] || exit 1
	sleep 0.1
done
exit 1`

func rcloneCreateCommand(config, remote, typ string, hasParameters bool) string {
	dir := lastPathPart.ReplaceAllString(config, "")
	if dir == "" {
		dir = "/"
	}
	prefix := "mkdir -p " + shellQuote(dir) + " && "
	if !hasParameters {
		return "command -v rclone >/dev/null 2>&1 || exit 92\n" + prefix + "rclone --config " + shellQuote(config) + " config create " +
			shellQuote(remote) + " " + shellQuote(typ) + "  --obscure --non-interactive >/dev/null"
	}
	return prefix + "{\n" + strings.Replace(rcloneCreateViaRC, "CONFIG_FILE", shellQuote(config), 1) + "\n}"
}

func createRemote(ctx context.Context, m *machine, config string, args RcloneRemoteArgs) error {
	var payload []byte
	hasParameters := len(args.Settings) != 0 || len(args.Secrets) != 0
	if hasParameters {
		var err error
		payload, err = rcloneCreatePayload(args.Remote, args.Type, args.Settings, args.Secrets)
		if err != nil {
			return fmt.Errorf("could not encode rclone config create request")
		}
	}
	command := rcloneCreateCommand(config, args.Remote, args.Type, hasParameters)
	code, err := m.runWithSecretInput(ctx, command, payload)
	if err != nil {
		return fmt.Errorf("rclone config create failed on %s: %w", m.describe(), err)
	}
	switch code {
	case 0:
		return nil
	case 90:
		return fmt.Errorf("openssl is not installed on %s", m.describe())
	case 91:
		return fmt.Errorf("curl is not installed on %s", m.describe())
	case 92:
		return fmt.Errorf("rclone is not installed on %s", m.describe())
	default:
		return fmt.Errorf("rclone config create failed on %s (exit %d)", m.describe(), code)
	}
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
		if err := createRemote(ctx, m, config, args); err != nil {
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
	// An import has only the id. It cannot tell a credential from a setting by looking, so the id
	// names the credential keys after an @ (remote@key1,key2), and they are read as secrets rather
	// than landing in the plain settings.
	id := req.ID
	var importSecrets []string
	if req.State.Remote == "" && req.Inputs.Remote == "" {
		if at := strings.LastIndex(id, "@"); at > 0 {
			id, importSecrets = id[:at], strings.Split(id[at+1:], ",")
		}
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
	secretKeys := jscompat.SortedKeys(state.Secrets)
	if importSecrets != nil {
		secretKeys = importSecrets
	}
	actual, err := readRemote(ctx, m, id, secretKeys, state.Config)
	if err != nil || actual == nil {
		return infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState]{}, err
	}
	secrets := maps.Clone(nonNil(state.Secrets))
	maps.Copy(secrets, actual.Secrets)
	state.Remote, state.Type, state.Settings, state.Secrets = id, actual.Type, actual.Settings, secrets
	inputs := req.Inputs
	if inputs.Remote == "" {
		config := state.Config
		inputs = RcloneRemoteArgs{Remote: id, Type: actual.Type, Settings: actual.Settings, Secrets: secrets, Config: &config}
	}
	return infer.ReadResponse[RcloneRemoteArgs, RcloneRemoteState]{ID: id, Inputs: inputs, State: state}, nil
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
		config = rcloneDefaultConfig
		if raw, ok := infer.GetRawRequest[p.DeleteRequest](ctx); ok && raw.Properties.Get("config").IsString() {
			config = raw.Properties.Get("config").AsString()
		}
	}
	stored, _, err := dumpRemote(ctx, m, req.ID, config)
	if err != nil || stored == nil {
		return infer.DeleteResponse{}, err
	}
	_, err = runRcloneConfig(ctx, m, config, "delete "+shellQuote(req.ID))
	return infer.DeleteResponse{}, err
}
