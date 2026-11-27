package homelab

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/resolved"
	"github.com/antimatter-studios/pulumi-homelab/provider/same"
	"github.com/antimatter-studios/pulumi-homelab/provider/sshd"
)

// SshdConfig is an sshd drop-in, validated with sshd -t before it is installed. Ported from
// src/resources/sshd.ts. It refuses to write into a directory sshd_config does not include, and
// renders its settings in the order the TypeScript wrote them on machines (provider/sshd
// RenderSshdInput and MatchFromInput), so the switch rewrites no drop-in.
type SshdConfig struct{}

// SshdSettings are the typed settings, named as sshd_config keywords in lower camel case.
type SshdSettings struct {
	PasswordAuthentication       *bool `pulumi:"passwordAuthentication,optional"`
	PubkeyAuthentication         *bool `pulumi:"pubkeyAuthentication,optional"`
	KbdInteractiveAuthentication *bool `pulumi:"kbdInteractiveAuthentication,optional"`
	PermitEmptyPasswords         *bool `pulumi:"permitEmptyPasswords,optional"`
	X11Forwarding                *bool `pulumi:"x11Forwarding,optional"`
	AllowAgentForwarding         *bool `pulumi:"allowAgentForwarding,optional"`
	PrintMotd                    *bool `pulumi:"printMotd,optional"`
	UsePAM                       *bool `pulumi:"usePAM,optional"`
	StrictModes                  *bool `pulumi:"strictModes,optional"`
	IgnoreRhosts                 *bool `pulumi:"ignoreRhosts,optional"`
	TcpKeepAlive                 *bool `pulumi:"tcpKeepAlive,optional"`
	UseDNS                       *bool `pulumi:"useDNS,optional"`

	PermitRootLogin    *string `pulumi:"permitRootLogin,optional"`
	AllowTcpForwarding *string `pulumi:"allowTcpForwarding,optional"`
	GatewayPorts       *string `pulumi:"gatewayPorts,optional"`
	LogLevel           *string `pulumi:"logLevel,optional"`
	Compression        *string `pulumi:"compression,optional"`

	Port                *float64 `pulumi:"port,optional"`
	MaxAuthTries        *float64 `pulumi:"maxAuthTries,optional"`
	MaxSessions         *float64 `pulumi:"maxSessions,optional"`
	LoginGraceTime      *float64 `pulumi:"loginGraceTime,optional"`
	ClientAliveInterval *float64 `pulumi:"clientAliveInterval,optional"`
	ClientAliveCountMax *float64 `pulumi:"clientAliveCountMax,optional"`

	AllowUsers    []string `pulumi:"allowUsers,optional"`
	AllowGroups   []string `pulumi:"allowGroups,optional"`
	DenyUsers     []string `pulumi:"denyUsers,optional"`
	DenyGroups    []string `pulumi:"denyGroups,optional"`
	ListenAddress []string `pulumi:"listenAddress,optional"`
	Ciphers       []string `pulumi:"ciphers,optional"`
	Macs          []string `pulumi:"macs,optional"`
	KexAlgorithms []string `pulumi:"kexAlgorithms,optional"`

	MaxStartups *string `pulumi:"maxStartups,optional"`
}

// SshdMatch are the Match criteria, keyed as sshd spells them.
type SshdMatch struct {
	Address      *string `pulumi:"Address,optional"`
	User         *string `pulumi:"User,optional"`
	Group        *string `pulumi:"Group,optional"`
	Host         *string `pulumi:"Host,optional"`
	LocalAddress *string `pulumi:"LocalAddress,optional"`
	LocalPort    *string `pulumi:"LocalPort,optional"`
}

// SshdConfigArgs are the inputs of an SshdConfig.
type SshdConfigArgs struct {
	File      *string           `pulumi:"file,optional"`
	Settings  *SshdSettings     `pulumi:"settings,optional"`
	Unchecked map[string]string `pulumi:"unchecked,optional"`
	Match     *SshdMatch        `pulumi:"match,optional"`
	Directory *string           `pulumi:"directory,optional"`
	Config    *string           `pulumi:"config,optional"`
}

// SshdConfigState is what is kept in state.
type SshdConfigState struct {
	File     string            `pulumi:"file"`
	Rendered map[string]string `pulumi:"rendered"`
	// Settings and Unchecked echo the inputs of the same name. The provider library writes an empty
	// list or map from the inputs back into the outputs, so the state has to be able to hold them,
	// or a program that declares one could never be read, updated or deleted again.
	Settings   *SshdSettings     `pulumi:"settings,optional"`
	Unchecked  map[string]string `pulumi:"unchecked,optional"`
	Match      map[string]string `pulumi:"match"`
	Directory  string            `pulumi:"directory"`
	Config     string            `pulumi:"config"`
	Effective  map[string]string `pulumi:"effective"`
	Overridden []string          `pulumi:"overridden"`
}

func (a *SshdConfigArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.File, "The drop-in's name without .conf. Defaults to 90-<resource name>.")
	an.Describe(&a.Settings, "Typed settings.")
	an.Describe(&a.Unchecked, "Keywords the typed settings do not name, written as given. One that repeats a typed keyword wins.")
	an.Describe(&a.Match, "Criteria scoping the settings to a Match block.")
	an.Describe(&a.Directory, "The drop-in directory, /etc/ssh/sshd_config.d by default. sshd_config must include it.")
	an.Describe(&a.Config, "The main configuration, /etc/ssh/sshd_config by default.")
}

func (s *SshdConfigState) Annotate(an infer.Annotator) {
	an.Describe(&s.Settings, "The rendered keywords and values.")
	an.Describe(&s.Effective, "What sshd -T resolves, every file merged.")
	an.Describe(&s.Overridden, "Keywords of this drop-in that sshd resolves differently.")
}

func (r *SshdConfig) Annotate(a infer.Annotator) {
	a.Describe(&r, "An sshd drop-in, validated before it is installed.")
	a.SetToken("index", "SshdConfig")
}

const (
	sshdDirectory = "/etc/ssh/sshd_config.d"
	sshdConfig    = "/etc/ssh/sshd_config"
)

func sshdPath(file, directory string) string { return directory + "/" + file + ".conf" }

// presentFields is a struct of optional fields as the input map a dynamic provider received:
// property name to value, unset fields absent.
func presentFields[T any](v *T) map[string]any {
	out := map[string]any{}
	if v == nil {
		return out
	}
	rv, rt := reflect.ValueOf(v).Elem(), reflect.TypeFor[T]()
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("pulumi"), ",")
		f := rv.Field(i)
		switch f.Kind() {
		case reflect.Pointer:
			if !f.IsNil() {
				out[name] = f.Elem().Interface()
			}
		case reflect.Slice:
			if !f.IsNil() {
				out[name] = f.Interface()
			}
		}
	}
	return out
}

// sshdRendered is the drop-in's keywords in production order, and as a map for state.
func sshdRendered(a SshdConfigArgs) ([]sshd.Keyword, map[string]string, []sshd.Criterion, map[string]string) {
	unchecked := a.Unchecked
	if unchecked == nil {
		unchecked = map[string]string{}
	}
	keywords := sshd.RenderSshdInput(presentFields(a.Settings), unchecked)
	settings := map[string]string{}
	for _, k := range keywords {
		settings[k.Keyword] = k.Value
	}
	match := map[string]string{}
	for name, value := range presentFields(a.Match) {
		match[name] = value.(string)
	}
	return keywords, settings, sshd.MatchFromInput(match), match
}

func sshdDropInOf(keywords []sshd.Keyword, criteria []sshd.Criterion) string {
	return sshd.SshdDropIn(keywords, criteria)
}

func sshdReadCommand(file, directory, config string) string {
	return "cat " + shellQuote(config) + " 2>/dev/null || true; echo '#pulumi-homelab#drop'; " +
		"cat " + shellQuote(sshdPath(file, directory)) + " 2>/dev/null || true; echo '#pulumi-homelab#effective'; " +
		"sshd -T 2>/dev/null || true"
}

type sshdRead struct {
	content   *string
	included  bool
	effective map[string]string
}

func parseSshdRead(out, directory string) sshdRead {
	main, rest := splitOnce(out, "#pulumi-homelab#drop\n")
	dropIn, effective := splitOnce(rest, "#pulumi-homelab#effective\n")
	read := sshdRead{included: sshd.HasInclude(main, directory), effective: sshd.ParseSshdT(effective)}
	if jscompat.Trim(dropIn) != "" {
		read.content = &dropIn
	}
	return read
}

func readSshdConfig(ctx context.Context, m *machine, file, directory, config string) (sshdRead, error) {
	asked, err := m.ask(ctx, m.escalate(sshdReadCommand(file, directory, config)))
	if err != nil {
		return sshdRead{}, err
	}
	if asked.code != 0 {
		return sshdRead{}, fmt.Errorf("could not read the sshd configuration: %s", jscompat.Trim(asked.err))
	}
	return parseSshdRead(asked.out, directory), nil
}

func sshdInstallCommand(file, content, directory string) string {
	return "mkdir -p " + shellQuote(directory) + " && staging=$(mktemp) && " +
		heredocInto(`"$staging"`, content) + "\n" +
		`sshd -t -f "$staging" || { rm -f "$staging"; echo "the resulting sshd configuration does not parse" >&2; exit 1; }; ` +
		`install -m 0644 -o root -g root "$staging" ` + shellQuote(sshdPath(file, directory)) + ` && rm -f "$staging" && ` +
		"systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true"
}

func sshdOverridden(settings, effective map[string]string) []string {
	return resolved.Disagreeing(settings, effective, jscompat.ToLower)
}

func settleSshd(ctx context.Context, m *machine, file string, a SshdConfigArgs) (SshdConfigState, error) {
	directory, config := orDefault(a.Directory, sshdDirectory), orDefault(a.Config, sshdConfig)
	keywords, settings, criteria, match := sshdRendered(a)
	content := sshdDropInOf(keywords, criteria)
	before, err := readSshdConfig(ctx, m, file, directory, config)
	if err != nil {
		return SshdConfigState{}, err
	}
	if !before.included {
		return SshdConfigState{}, fmt.Errorf("%s on %s does not include %s, so a drop-in written there "+
			"would be read by nothing. Add `Include %s/*.conf` to %s first — a file "+
			"that is never read looks exactly like a setting that was applied and did not take effect.", config, m.describe(), directory, directory, config)
	}
	if before.content == nil || *before.content != content {
		if _, err := m.must(ctx, m.escalate(sshdInstallCommand(file, content, directory))); err != nil {
			return SshdConfigState{}, err
		}
	}
	actual, err := readSshdConfig(ctx, m, file, directory, config)
	if err != nil {
		return SshdConfigState{}, err
	}
	return SshdConfigState{File: file, Rendered: settings, Settings: a.Settings, Unchecked: a.Unchecked, Match: match, Directory: directory, Config: config,
		Effective: actual.effective, Overridden: sshdOverridden(settings, actual.effective)}, nil
}

// sshdArgsFromDropIn recovers the inputs that render a drop-in byte for byte, for an import:
// each keyword a typed setting names, with a value that typed setting renders back exactly, is
// typed; the rest are unchecked. Rendering puts typed keywords first, then unchecked ones, each
// sorted, so a drop-in the TypeScript wrote comes back as it was. The boolean indicates whether
// the original declaration could be reconstructed.
func sshdArgsFromDropIn(content string) (SshdConfigArgs, bool) {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(lines) < 1 || lines[0] != "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`." {
		return SshdConfigArgs{}, false
	}
	args := SshdConfigArgs{Settings: &SshdSettings{}, Unchecked: map[string]string{}}
	body := lines[1:]
	if len(body) > 0 && strings.HasPrefix(body[0], "Match ") {
		words := strings.Split(strings.TrimPrefix(body[0], "Match "), " ")
		if len(words)%2 != 0 {
			return SshdConfigArgs{}, false
		}
		args.Match = &SshdMatch{}
		for i := 0; i < len(words); i += 2 {
			field := reflect.ValueOf(args.Match).Elem().FieldByName(words[i])
			if !field.IsValid() {
				return SshdConfigArgs{}, false
			}
			v := words[i+1]
			field.Set(reflect.ValueOf(&v))
		}
		body = body[1:]
	}
	if len(body) == 1 && body[0] == "" {
		body = nil
	}
	typed := map[string]int{}
	rt := reflect.TypeFor[SshdSettings]()
	for i := 0; i < rt.NumField(); i++ {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("pulumi"), ",")
		typed[sshd.KeywordFor(name)] = i
	}
	type entry struct{ keyword, value string }
	var entries []entry
	for _, line := range body {
		if args.Match != nil {
			line = strings.TrimPrefix(line, "    ")
		}
		keyword, value, found := strings.Cut(line, " ")
		if !found || keyword == "" {
			return SshdConfigArgs{}, false
		}
		entries = append(entries, entry{keyword, value})
	}
	// The renderer writes typed settings, then unchecked keywords. Where that boundary was is not in
	// the file, so every split is tried, most typed first, and the first that renders the file back
	// byte for byte is the import: a keyword a typed setting could hold may have been declared
	// unchecked, and promoting it would reorder the file (review of #98).
	for split := len(entries); split >= 0; split-- {
		attempt := SshdConfigArgs{Settings: &SshdSettings{}, Unchecked: map[string]string{}, Match: args.Match}
		settings := reflect.ValueOf(attempt.Settings).Elem()
		ok := true
		for i, e := range entries {
			if i < split {
				field, known := typed[e.keyword]
				if !known || !setTyped(settings.Field(field), e.value) {
					ok = false
					break
				}
				continue
			}
			attempt.Unchecked[e.keyword] = e.value
		}
		if !ok {
			continue
		}
		keywords, _, criteria, _ := sshdRendered(attempt)
		if sshdDropInOf(keywords, criteria) == content {
			return attempt, true
		}
	}
	return SshdConfigArgs{}, false
}

// setTyped sets a typed setting from its rendered value, when the setting renders it back exactly.
func setTyped(field reflect.Value, value string) bool {
	switch field.Type().Elem().Kind() {
	case reflect.Bool:
		if value != "yes" && value != "no" {
			return false
		}
		b := value == "yes"
		field.Set(reflect.ValueOf(&b))
	case reflect.Float64:
		n := jscompat.Number(value)
		if value == "" || jscompat.FormatNumber(n) != value {
			return false
		}
		field.Set(reflect.ValueOf(&n))
	case reflect.String:
		v := value
		field.Set(reflect.ValueOf(&v))
	default: // []string
		field.Set(reflect.ValueOf(strings.Split(value, ",")))
	}
	return true
}

// Check defaults the file to 90-<name> and the paths, as the TypeScript constructor did.
func (SshdConfig) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SshdConfigArgs], error) {
	args, failures, err := infer.DefaultCheck[SshdConfigArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[SshdConfigArgs]{Inputs: args, Failures: failures}, err
	}
	if args.File == nil {
		file := "90-" + req.Name
		args.File = &file
	}
	if args.Directory == nil {
		d := sshdDirectory
		args.Directory = &d
	}
	if args.Config == nil {
		c := sshdConfig
		args.Config = &c
	}
	return infer.CheckResponse[SshdConfigArgs]{Inputs: args}, nil
}

// Create installs the drop-in; the id is the file.
func (SshdConfig) Create(ctx context.Context, req infer.CreateRequest[SshdConfigArgs]) (infer.CreateResponse[SshdConfigState], error) {
	file := deref(req.Inputs.File)
	if req.DryRun {
		_, settings, _, match := sshdRendered(req.Inputs)
		return infer.CreateResponse[SshdConfigState]{ID: file, Output: SshdConfigState{File: file, Rendered: settings, Settings: req.Inputs.Settings, Unchecked: req.Inputs.Unchecked, Match: match,
			Directory: orDefault(req.Inputs.Directory, sshdDirectory), Config: orDefault(req.Inputs.Config, sshdConfig), Effective: map[string]string{}, Overridden: []string{}}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SshdConfigState]{}, err
	}
	state, err := settleSshd(ctx, m, file, req.Inputs)
	if err != nil {
		return infer.CreateResponse[SshdConfigState]{}, err
	}
	return infer.CreateResponse[SshdConfigState]{ID: file, Output: state}, nil
}

// Read reports what sshd resolves; the drop-in gone or empty, it is forgotten.
func (SshdConfig) Read(ctx context.Context, req infer.ReadRequest[SshdConfigArgs, SshdConfigState]) (infer.ReadResponse[SshdConfigArgs, SshdConfigState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SshdConfigArgs, SshdConfigState]{}, err
	}
	state := req.State
	imported := state.File == ""
	if imported {
		state.Directory, state.Config = sshdDirectory, sshdConfig
	}
	actual, err := readSshdConfig(ctx, m, req.ID, state.Directory, state.Config)
	if err != nil || actual.content == nil {
		return infer.ReadResponse[SshdConfigArgs, SshdConfigState]{}, err
	}
	inputs := req.Inputs
	if imported {
		parsed, ok := sshdArgsFromDropIn(*actual.content)
		if ok {
			_, state.Rendered, _, state.Match = sshdRendered(parsed)
			state.Settings, state.Unchecked = parsed.Settings, parsed.Unchecked
		}
		parsed.File, parsed.Directory, parsed.Config = &req.ID, &state.Directory, &state.Config
		inputs = parsed
	}
	if state.Rendered == nil {
		state.Rendered = map[string]string{}
	}
	if state.Match == nil {
		state.Match = map[string]string{}
	}
	state.File, state.Effective = req.ID, actual.effective
	state.Overridden = sshdOverridden(state.Rendered, actual.effective)
	return infer.ReadResponse[SshdConfigArgs, SshdConfigState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update installs the drop-in again under its id.
func (SshdConfig) Update(ctx context.Context, req infer.UpdateRequest[SshdConfigArgs, SshdConfigState]) (infer.UpdateResponse[SshdConfigState], error) {
	if req.DryRun {
		s := req.State
		_, s.Rendered, _, s.Match = sshdRendered(req.Inputs)
		s.Settings, s.Unchecked = req.Inputs.Settings, req.Inputs.Unchecked
		return infer.UpdateResponse[SshdConfigState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SshdConfigState]{}, err
	}
	state, err := settleSshd(ctx, m, req.ID, req.Inputs)
	return infer.UpdateResponse[SshdConfigState]{Output: state}, err
}

// Diff compares the rendered keywords and the Match criteria; another file replaces.
func (SshdConfig) Diff(_ context.Context, req infer.DiffRequest[SshdConfigArgs, SshdConfigState]) (p.DiffResponse, error) {
	old := req.State
	_, settings, _, match := sshdRendered(req.Inputs)
	file := old.File
	if req.Inputs.File != nil {
		file = *req.Inputs.File
	}
	oldSettings, oldMatch := old.Rendered, old.Match
	if oldSettings == nil {
		oldSettings = map[string]string{}
	}
	if oldMatch == nil {
		oldMatch = map[string]string{}
	}
	changed := !same.SameJSON(oldSettings, settings) || !same.SameJSON(oldMatch, match)
	var replaces []string
	if old.File != file {
		replaces = []string{"file"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete removes the drop-in from the directory in state and reloads sshd.
func (SshdConfig) Delete(ctx context.Context, req infer.DeleteRequest[SshdConfigState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate("rm -f "+shellQuote(sshdPath(req.ID, req.State.Directory))+" && "+
		"systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true"))
	return infer.DeleteResponse{}, err
}
