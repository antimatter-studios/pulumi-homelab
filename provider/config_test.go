package homelab

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// homelabServerConfig is equivalent to homelab-server's stated policy: trove agent via
// CHORE_AGENT_SOCK, host keys pinned, ~/.ssh off, strict checking, sudo -n.
func homelabServerConfig() map[string]property.Value {
	return map[string]property.Value{
		"address":               property.New("127.0.0.1"),
		"port":                  property.New(20022.0),
		"user":                  property.New("chris"),
		"agentSocketEnv":        property.New("CHORE_AGENT_SOCK"),
		"hostKeys":              property.New([]property.Value{property.New("ssh-ed25519 AAAAhost")}),
		"readSshConfig":         property.New(false),
		"readDefaultKeys":       property.New(false),
		"readKnownHosts":        property.New(false),
		"strictHostKeyChecking": property.New(true),
		"escalation":            property.New("sudo"),
	}
}

// capture records the remote.Config the factory was given and hands back runner.
type capture struct {
	got    *remote.Config
	runner remote.Runner
}

func (c *capture) factory(_ context.Context, cfg remote.Config) (remote.Runner, error) {
	c.got = &cfg
	return c.runner, nil
}

func newServer(t *testing.T, factory remote.Factory) integration.Server {
	t.Helper()
	s, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(ProviderWith(factory)))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func configure(t *testing.T, factory remote.Factory, cfg map[string]property.Value) error {
	t.Helper()
	return newServer(t, factory).Configure(p.ConfigureRequest{Args: property.NewMap(cfg)})
}

func checkConfig(t *testing.T, cfg map[string]property.Value) error {
	t.Helper()
	resp, err := newServer(t, nil).CheckConfig(p.CheckRequest{Urn: "urn:pulumi:s::p::pulumi:providers:homelab::default", Inputs: property.NewMap(cfg)})
	if err != nil {
		return err
	}
	if len(resp.Failures) > 0 {
		var msgs []string
		for _, f := range resp.Failures {
			msgs = append(msgs, f.Property+": "+f.Reason)
		}
		return errors.New(strings.Join(msgs, "; "))
	}
	return nil
}

func without(cfg map[string]property.Value, keys ...string) map[string]property.Value {
	out := maps.Clone(cfg)
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

func with(cfg map[string]property.Value, k string, v property.Value) map[string]property.Value {
	out := maps.Clone(cfg)
	out[k] = v
	return out
}

func TestValidConfigurationIsAccepted(t *testing.T) {
	c := &capture{runner: remotetest.New()}
	if err := checkConfig(t, homelabServerConfig()); err != nil {
		t.Fatalf("CheckConfig: %v", err)
	}
	if err := configure(t, c.factory, homelabServerConfig()); err != nil {
		t.Fatalf("Configure: %v", err)
	}
}

// Each missing required key fails Configure and CheckConfig with an error naming it.
func TestMissingRequiredKeyIsNamed(t *testing.T) {
	cases := []struct {
		drop    []string
		mention []string
	}{
		{[]string{"address"}, []string{"homelab:address"}},
		{[]string{"port"}, []string{"homelab:port"}},
		{[]string{"user"}, []string{"homelab:user"}},
		{[]string{"agentSocketEnv"}, []string{"homelab:agentSocket", "homelab:agentSocketEnv"}},
		{[]string{"hostKeys"}, []string{"homelab:hostKeys", "homelab:hostKeysFile"}},
		{[]string{"readSshConfig"}, []string{"homelab:readSshConfig"}},
		{[]string{"readDefaultKeys"}, []string{"homelab:readDefaultKeys"}},
		{[]string{"readKnownHosts"}, []string{"homelab:readKnownHosts"}},
		{[]string{"strictHostKeyChecking"}, []string{"homelab:strictHostKeyChecking"}},
		{[]string{"escalation"}, []string{"homelab:escalation"}},
	}
	for _, c := range cases {
		t.Run(c.drop[0], func(t *testing.T) {
			cfg := without(homelabServerConfig(), c.drop...)
			factoryCalled := false
			f := func(context.Context, remote.Config) (remote.Runner, error) {
				factoryCalled = true
				return remotetest.New(), nil
			}
			for name, err := range map[string]error{"Configure": configure(t, f, cfg), "CheckConfig": checkConfig(t, cfg)} {
				if err == nil {
					t.Fatalf("%s accepted a configuration without %v", name, c.drop)
				}
				for _, m := range c.mention {
					if !strings.Contains(err.Error(), m) {
						t.Errorf("%s error %q does not name %s", name, err, m)
					}
				}
			}
			if factoryCalled {
				t.Error("the runner was built from an invalid configuration")
			}
		})
	}
}

func TestSudoPasswordIsRequiredForSudoPasswordEscalation(t *testing.T) {
	cfg := with(homelabServerConfig(), "escalation", property.New("sudo-password"))
	for name, err := range map[string]error{"Configure": configure(t, nil, cfg), "CheckConfig": checkConfig(t, cfg)} {
		if err == nil || !strings.Contains(err.Error(), "homelab:sudoPassword") {
			t.Errorf("%s: got %v, want an error naming homelab:sudoPassword", name, err)
		}
	}
}

func TestMissingJumpFieldsAreNamed(t *testing.T) {
	cfg := with(homelabServerConfig(), "jumps", property.New([]property.Value{property.New(property.NewMap(map[string]property.Value{
		"address": property.New(""), "port": property.New(22.0), "user": property.New("admin"),
	}))}))
	err := configure(t, nil, cfg)
	if err == nil || !strings.Contains(err.Error(), "homelab:jumps[0].address") {
		t.Fatalf("got %v, want an error naming homelab:jumps[0].address", err)
	}
}

// There is no fallback between sources: giving both is an error, not a preference.
func TestBothSourcesAreRefused(t *testing.T) {
	both := with(homelabServerConfig(), "agentSocket", property.New("/run/agent.sock"))
	if err := configure(t, nil, both); err == nil || !strings.Contains(err.Error(), "homelab:agentSocket") {
		t.Errorf("agent socket: got %v", err)
	}
	both = with(homelabServerConfig(), "hostKeysFile", property.New("hosts/pi.known_hosts"))
	if err := configure(t, nil, both); err == nil || !strings.Contains(err.Error(), "homelab:hostKeysFile") {
		t.Errorf("host keys: got %v", err)
	}
}

func TestUnknownEscalationIsRefused(t *testing.T) {
	err := configure(t, nil, with(homelabServerConfig(), "escalation", property.New("doas")))
	if err == nil || !strings.Contains(err.Error(), "homelab:escalation") {
		t.Fatalf("got %v", err)
	}
}

// Without an injected factory, the provider says the transport is not built yet.
func TestRealFactorySaysTransportIsNotBuilt(t *testing.T) {
	err := configure(t, nil, homelabServerConfig())
	if err == nil || !strings.Contains(err.Error(), "#34") {
		t.Fatalf("got %v", err)
	}
}

// Each setting is carried, as configured, into the runner configuration.
func TestEverySettingIsHonoured(t *testing.T) {
	full := map[string]property.Value{
		"address": property.New("192.0.2.10"),
		"port":    property.New(2222.0),
		"user":    property.New("deploy"),
		"jumps": property.New([]property.Value{property.New(property.NewMap(map[string]property.Value{
			"address":  property.New("bastion.example"),
			"port":     property.New(22.0),
			"user":     property.New("admin"),
			"hostKeys": property.New([]property.Value{property.New("ssh-ed25519 AAAAjump")}),
		}))}),
		"agentSocket":           property.New("/run/trove.sock"),
		"identity":              property.New("PRIVATE KEY").WithSecret(true),
		"hostKeysFile":          property.New("hosts/pi.known_hosts"),
		"readSshConfig":         property.New(true),
		"readDefaultKeys":       property.New(true),
		"readKnownHosts":        property.New(true),
		"strictHostKeyChecking": property.New(false),
		"escalation":            property.New("sudo-password"),
		"sudoPassword":          property.New("hunter2").WithSecret(true),
	}
	c := &capture{runner: remotetest.New()}
	if err := configure(t, c.factory, full); err != nil {
		t.Fatal(err)
	}
	want := remote.Config{
		Address: "192.0.2.10", Port: 2222, User: "deploy",
		Jumps:                 []remote.Jump{{Address: "bastion.example", Port: 22, User: "admin", HostKeys: []string{"ssh-ed25519 AAAAjump"}}},
		AgentSocket:           "/run/trove.sock",
		Identity:              "PRIVATE KEY",
		HostKeysFile:          "hosts/pi.known_hosts",
		ReadSSHConfig:         true,
		ReadDefaultKeys:       true,
		ReadKnownHosts:        true,
		StrictHostKeyChecking: false,
		Escalation:            "sudo-password",
		SudoPassword:          "hunter2",
	}
	if !reflect.DeepEqual(*c.got, want) {
		t.Fatalf("runner config\n got %+v\nwant %+v", *c.got, want)
	}

	// and the other side of each either/or, and each boolean the other way
	c = &capture{runner: remotetest.New()}
	if err := configure(t, c.factory, homelabServerConfig()); err != nil {
		t.Fatal(err)
	}
	want = remote.Config{
		Address: "127.0.0.1", Port: 20022, User: "chris", Jumps: []remote.Jump{},
		AgentSocketEnv: "CHORE_AGENT_SOCK", HostKeys: []string{"ssh-ed25519 AAAAhost"},
		StrictHostKeyChecking: true, Escalation: "sudo",
	}
	got := *c.got
	if len(got.Jumps) == 0 {
		got.Jumps = []remote.Jump{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("runner config\n got %+v\nwant %+v", got, want)
	}
}

// Nothing is resolved from the environment or the home directory: the env var's name is carried,
// not its value, even when the process has it (and SSH_AUTH_SOCK) set.
func TestNothingIsReadFromTheEnvironment(t *testing.T) {
	t.Setenv("CHORE_AGENT_SOCK", "/should/not/be/read")
	t.Setenv("SSH_AUTH_SOCK", "/booby/trap")
	t.Setenv("HOME", t.TempDir())
	c := &capture{runner: remotetest.New()}
	if err := configure(t, c.factory, homelabServerConfig()); err != nil {
		t.Fatal(err)
	}
	if c.got.AgentSocket != "" || c.got.AgentSocketEnv != "CHORE_AGENT_SOCK" {
		t.Fatalf("agent socket was resolved: %+v", c.got)
	}
	if c.got.ReadSSHConfig || c.got.ReadDefaultKeys || c.got.ReadKnownHosts {
		t.Fatalf("~/.ssh switched on: %+v", c.got)
	}
}

// A change to the route, auth or host keys alone is never a provider replacement.
func TestRouteChangeIsNotAReplacement(t *testing.T) {
	old := homelabServerConfig()
	changes := map[string]property.Value{
		"address":        property.New("192.168.0.47"),
		"port":           property.New(22.0),
		"user":           property.New("admin"),
		"agentSocketEnv": property.New("OTHER_SOCK"),
		"hostKeys":       property.New([]property.Value{property.New("ssh-ed25519 AAAAother")}),
		"identity":       property.New("KEY").WithSecret(true),
		"readKnownHosts": property.New(true),
		"jumps": property.New([]property.Value{property.New(property.NewMap(map[string]property.Value{
			"address": property.New("s2.example"), "port": property.New(22.0), "user": property.New("admin"),
		}))}),
	}
	s := newServer(t, nil)
	for k, v := range changes {
		t.Run(k, func(t *testing.T) {
			resp, err := s.DiffConfig(p.DiffRequest{
				Urn:       "urn:pulumi:s::p::pulumi:providers:homelab::pi",
				State:     property.NewMap(old),
				OldInputs: property.NewMap(old),
				Inputs:    property.NewMap(with(old, k, v)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.HasChanges {
				t.Errorf("a change to %s was not seen", k)
			}
			for path, d := range resp.DetailedDiff {
				switch d.Kind {
				case p.AddReplace, p.UpdateReplace, p.DeleteReplace:
					t.Errorf("a change to %s replaces the provider (%s: %s)", k, path, d.Kind)
				}
			}
		})
	}
}

var groupURN = presource.URN("urn:pulumi:s::p::homelab:index:Group::storage")

// A route-only change gives no resource diff: Group state holds no route field, and a provider
// configured with a different route diffs the same state as unchanged.
func TestRouteOnlyChangeGivesNoResourceDiff(t *testing.T) {
	fake := remotetest.New().
		Once("getent group 'storage'", remotetest.Reply{Code: 2}).
		On(`sudo -n sh -c 'groupadd -g 1002 '\''storage'\'''`, remotetest.Reply{}).
		On("getent group 'storage'", remotetest.Reply{Stdout: "storage:x:1002:admin\n"})
	first := newServer(t, (&capture{runner: fake}).factory)
	if err := first.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := property.NewMap(map[string]property.Value{"name": property.New("storage"), "gid": property.New(1002.0)})
	created, err := first.Create(p.CreateRequest{Urn: groupURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	route := map[string]bool{}
	for k := range homelabServerConfig() {
		route[k] = true
	}
	route["jumps"], route["identity"], route["agentSocket"], route["hostKeysFile"], route["sudoPassword"] = true, true, true, true, true
	for k := range created.Properties.All() {
		if route[k] {
			t.Errorf("Group state stores the route field %q", k)
		}
	}

	moved := with(with(with(homelabServerConfig(), "address", property.New("192.168.0.47")), "port", property.New(22.0)),
		"jumps", property.New([]property.Value{property.New(property.NewMap(map[string]property.Value{
			"address": property.New("s2.example"), "port": property.New(22.0), "user": property.New("admin"),
			"hostKeys": property.New([]property.Value{property.New("ssh-ed25519 AAAAjump")}),
		}))}))
	second := newServer(t, (&capture{runner: remotetest.New()}).factory)
	if err := second.Configure(p.ConfigureRequest{Args: property.NewMap(moved)}); err != nil {
		t.Fatal(err)
	}
	diff, err := second.Diff(p.DiffRequest{ID: created.ID, Urn: groupURN, State: created.Properties, OldInputs: inputs, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff.HasChanges || len(diff.DetailedDiff) != 0 {
		t.Fatalf("a route-only change diffs the resource: %+v", diff)
	}
}

// With strict checking and known_hosts off, a jump without pinned keys is refused by name at
// configure time rather than at connect time.
func TestStrictJumpNeedsPinnedKeys(t *testing.T) {
	jump := property.New(property.NewMap(map[string]property.Value{
		"address": property.New("s2.antimatter-studios.com"),
		"port":    property.New(22.0),
		"user":    property.New("admin"),
	}))
	err := configure(t, nil, with(homelabServerConfig(), "jumps", property.New([]property.Value{jump})))
	if err == nil || !strings.Contains(err.Error(), "homelab:jumps[0].hostKeys") {
		t.Fatalf("got %v", err)
	}
	// not required when strict checking is off
	loose := with(with(homelabServerConfig(), "jumps", property.New([]property.Value{jump})), "strictHostKeyChecking", property.New(false))
	c := &capture{runner: remotetest.New()}
	if err := configure(t, c.factory, loose); err != nil {
		t.Fatal(err)
	}
}
