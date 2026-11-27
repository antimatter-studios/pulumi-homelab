package homelab

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// Jump is one hop of the route to the machine.
type Jump struct {
	Address  string   `pulumi:"address"`
	Port     int      `pulumi:"port"`
	User     string   `pulumi:"user"`
	HostKeys []string `pulumi:"hostKeys,optional"`
}

// Config is the provider configuration: the whole connection and ssh policy, read only from the
// project's homelab:* stack config or an explicit provider's arguments (owner decision on #33).
//
// Nothing here has a default, nothing reads the environment or the home directory, and nothing
// falls back from one source to another. Every setting is declared optional in the schema only so
// that a missing one can be reported by its config key (homelab:address) rather than by a
// generic decoding failure; Check and Configure enforce them.
type Config struct {
	Address *string `pulumi:"address,optional"`
	Port    *int    `pulumi:"port,optional"`
	User    *string `pulumi:"user,optional"`
	Jumps   []Jump  `pulumi:"jumps,optional"`

	AgentSocket    *string `pulumi:"agentSocket,optional"`
	AgentSocketEnv *string `pulumi:"agentSocketEnv,optional"`

	Identity *string `pulumi:"identity,optional" provider:"secret"`

	HostKeys     []string `pulumi:"hostKeys,optional"`
	HostKeysFile *string  `pulumi:"hostKeysFile,optional"`

	ReadSSHConfig         *bool `pulumi:"readSshConfig,optional"`
	ReadDefaultKeys       *bool `pulumi:"readDefaultKeys,optional"`
	ReadKnownHosts        *bool `pulumi:"readKnownHosts,optional"`
	StrictHostKeyChecking *bool `pulumi:"strictHostKeyChecking,optional"`

	Escalation   *string `pulumi:"escalation,optional"`
	SudoPassword *string `pulumi:"sudoPassword,optional" provider:"secret"`

	factory remote.Factory
	runner  remote.Runner
	machine *machine
}

// Annotate documents every key.
func (c *Config) Annotate(a infer.Annotator) {
	a.Describe(&c.Address, "Address of the managed machine. Required.")
	a.Describe(&c.Port, "ssh port of the managed machine. Required.")
	a.Describe(&c.User, "User to connect as. Required.")
	a.Describe(&c.Jumps, "Hops to go through, in order, each with its own address, port, user and pinned host keys.")
	a.Describe(&c.AgentSocket, "Path of the ssh agent socket. Exactly one of agentSocket and agentSocketEnv is required.")
	a.Describe(&c.AgentSocketEnv, "Name of the environment variable holding the agent socket path, e.g. CHORE_AGENT_SOCK.")
	a.Describe(&c.Identity, "Optional explicit private key to authenticate with.")
	a.Describe(&c.HostKeys, "Pinned host keys of the machine. Exactly one of hostKeys and hostKeysFile is required.")
	a.Describe(&c.HostKeysFile, "Project file holding the machine's host keys, in known_hosts format.")
	a.Describe(&c.ReadSSHConfig, "Whether ~/.ssh/config may be read. Required.")
	a.Describe(&c.ReadDefaultKeys, "Whether the default key files (~/.ssh/id_*) may be read. Required.")
	a.Describe(&c.ReadKnownHosts, "Whether ~/.ssh/known_hosts may be read. Required.")
	a.Describe(&c.StrictHostKeyChecking, "Whether an unknown or changed host key is refused. Required.")
	a.Describe(&c.Escalation, "How commands become root: \"sudo\" (sudo -n), \"none\" (already root) or \"sudo-password\" (sudo -S with sudoPassword). Required.")
	a.Describe(&c.SudoPassword, "The sudo password, required when escalation is \"sudo-password\" and refused otherwise.")
}

func key(name string) string { return Name + ":" + name }

func missing(name string) error {
	return fmt.Errorf("missing required configuration %s", key(name))
}

// validate reports the first problem with the configuration, naming the config key.
func (c *Config) validate() error {
	var errs []error
	if c.Address == nil || *c.Address == "" {
		errs = append(errs, missing("address"))
	}
	if c.Port == nil {
		errs = append(errs, missing("port"))
	} else if *c.Port < 1 || *c.Port > 65535 {
		errs = append(errs, fmt.Errorf("%s must be between 1 and 65535, got %d", key("port"), *c.Port))
	}
	if c.User == nil || *c.User == "" {
		errs = append(errs, missing("user"))
	}
	for i, j := range c.Jumps {
		at := fmt.Sprintf("jumps[%d]", i)
		if j.Address == "" {
			errs = append(errs, missing(at+".address"))
		}
		if j.Port < 1 || j.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s must be between 1 and 65535, got %d", key(at+".port"), j.Port))
		}
		if j.User == "" {
			errs = append(errs, missing(at+".user"))
		}
	}
	switch {
	case c.AgentSocket == nil && c.AgentSocketEnv == nil:
		errs = append(errs, fmt.Errorf("missing required configuration %s or %s", key("agentSocket"), key("agentSocketEnv")))
	case c.AgentSocket != nil && c.AgentSocketEnv != nil:
		errs = append(errs, fmt.Errorf("%s and %s are both set; give exactly one", key("agentSocket"), key("agentSocketEnv")))
	case c.AgentSocket != nil && *c.AgentSocket == "":
		errs = append(errs, missing("agentSocket"))
	case c.AgentSocketEnv != nil && *c.AgentSocketEnv == "":
		errs = append(errs, missing("agentSocketEnv"))
	}
	switch {
	case c.HostKeys == nil && c.HostKeysFile == nil:
		errs = append(errs, fmt.Errorf("missing required configuration %s or %s", key("hostKeys"), key("hostKeysFile")))
	case c.HostKeys != nil && c.HostKeysFile != nil:
		errs = append(errs, fmt.Errorf("%s and %s are both set; give exactly one", key("hostKeys"), key("hostKeysFile")))
	case c.HostKeys != nil && len(c.HostKeys) == 0:
		errs = append(errs, missing("hostKeys"))
	case c.HostKeysFile != nil && *c.HostKeysFile == "":
		errs = append(errs, missing("hostKeysFile"))
	}
	if c.ReadSSHConfig == nil {
		errs = append(errs, missing("readSshConfig"))
	}
	if c.ReadDefaultKeys == nil {
		errs = append(errs, missing("readDefaultKeys"))
	}
	if c.ReadKnownHosts == nil {
		errs = append(errs, missing("readKnownHosts"))
	}
	if c.StrictHostKeyChecking == nil {
		errs = append(errs, missing("strictHostKeyChecking"))
	}
	if c.Escalation == nil {
		errs = append(errs, missing("escalation"))
	} else {
		switch *c.Escalation {
		case "sudo", "none":
			if c.SudoPassword != nil {
				errs = append(errs, fmt.Errorf("%s is set but %s is %q; it is only used with \"sudo-password\"", key("sudoPassword"), key("escalation"), *c.Escalation))
			}
		case "sudo-password":
			if c.SudoPassword == nil || *c.SudoPassword == "" {
				errs = append(errs, missing("sudoPassword"))
			}
		default:
			errs = append(errs, fmt.Errorf("%s must be \"sudo\", \"none\" or \"sudo-password\", got %q", key("escalation"), *c.Escalation))
		}
	}
	return errors.Join(errs...)
}

func deref[T any](v *T) T {
	if v == nil {
		var zero T
		return zero
	}
	return *v
}

// RunnerConfig is the configuration handed to the transport, carried over as configured.
func (c *Config) RunnerConfig() remote.Config {
	jumps := make([]remote.Jump, len(c.Jumps))
	for i, j := range c.Jumps {
		jumps[i] = remote.Jump{Address: j.Address, Port: j.Port, User: j.User, HostKeys: j.HostKeys}
	}
	return remote.Config{
		Address:               deref(c.Address),
		Port:                  deref(c.Port),
		User:                  deref(c.User),
		Jumps:                 jumps,
		AgentSocket:           deref(c.AgentSocket),
		AgentSocketEnv:        deref(c.AgentSocketEnv),
		Identity:              deref(c.Identity),
		HostKeys:              c.HostKeys,
		HostKeysFile:          deref(c.HostKeysFile),
		ReadSSHConfig:         deref(c.ReadSSHConfig),
		ReadDefaultKeys:       deref(c.ReadDefaultKeys),
		ReadKnownHosts:        deref(c.ReadKnownHosts),
		StrictHostKeyChecking: deref(c.StrictHostKeyChecking),
		Escalation:            deref(c.Escalation),
		SudoPassword:          deref(c.SudoPassword),
	}
}

// Check validates the configuration, failing with an error that names the missing key.
func (c *Config) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[*Config], error) {
	cfg, failures, err := infer.DefaultCheck[*Config](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[*Config]{}, err
	}
	if len(failures) == 0 {
		if err := cfg.validate(); err != nil {
			return infer.CheckResponse[*Config]{}, err
		}
	}
	return infer.CheckResponse[*Config]{Inputs: cfg, Failures: failures}, nil
}

// Diff never asks for a replacement. Every setting is about how the machine is reached, not what
// is on it, so a change to any of them is an in-place update of the provider and resources keep
// their state.
func (c *Config) Diff(_ context.Context, req infer.DiffRequest[*Config, *Config]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	olds, news := reflect.ValueOf(req.State).Elem(), reflect.ValueOf(req.Inputs).Elem()
	t := olds.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if !reflect.DeepEqual(olds.Field(i).Interface(), news.Field(i).Interface()) {
			name := f.Tag.Get("pulumi")
			for j, r := range name {
				if r == ',' {
					name = name[:j]
					break
				}
			}
			resp.DetailedDiff[name] = p.PropertyDiff{Kind: p.Update, InputDiff: true}
			resp.HasChanges = true
		}
	}
	return resp, nil
}

// Configure validates the configuration and builds the Runner from it.
func (c *Config) Configure(ctx context.Context) error {
	if err := c.validate(); err != nil {
		return err
	}
	factory := c.factory
	if factory == nil {
		factory = transportNotBuilt
	}
	runner, err := factory(ctx, c.RunnerConfig())
	if err != nil {
		return err
	}
	c.runner = runner
	c.machine = &machine{runner: runner, cfg: c.RunnerConfig()}
	return nil
}

// transportNotBuilt is the real factory until the ssh transport lands.
func transportNotBuilt(context.Context, remote.Config) (remote.Runner, error) {
	return nil, errors.New("the ssh transport is not built yet (#34)")
}
