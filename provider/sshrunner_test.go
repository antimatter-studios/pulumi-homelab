package homelab

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	"github.com/antimatter-studios/pulumi-homelab/provider/transport"
)

func noEnv(string) (string, bool)   { return "", false }
func noFile(string) ([]byte, error) { return nil, errors.New("no file") }
func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func homelabServerRunnerConfig() remote.Config {
	return remote.Config{
		Address: "127.0.0.1", Port: 20022, User: "chris",
		Jumps:                 []remote.Jump{{Address: "s2.antimatter-studios.com", Port: 22, User: "admin", HostKeys: []string{"ssh-ed25519 AAAAjump"}}},
		AgentSocketEnv:        "CHORE_AGENT_SOCK",
		HostKeys:              []string{"ssh-ed25519 AAAAhost"},
		StrictHostKeyChecking: true,
		Escalation:            "sudo",
	}
}

// The configuration becomes the transport's route, socket and pins, and nothing else.
func TestTransportConfigFromHomelabServer(t *testing.T) {
	got, err := transportConfig(homelabServerRunnerConfig(), env(map[string]string{"CHORE_AGENT_SOCK": "/run/chore.sock", "SSH_AUTH_SOCK": "/trap"}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	want := transport.Config{
		Hops: []transport.Hop{
			{Address: "s2.antimatter-studios.com", Port: 22, User: "admin"},
			{Address: "127.0.0.1", Port: 20022, User: "chris"},
		},
		AgentSocket: "/run/chore.sock",
		HostKeys: []string{
			"s2.antimatter-studios.com ssh-ed25519 AAAAjump",
			"[127.0.0.1]:20022 ssh-ed25519 AAAAhost",
		},
		StrictHostKeyChecking: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

// An explicit socket path is used as given; the environment is not consulted at all.
func TestExplicitAgentSocketIgnoresTheEnvironment(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.AgentSocketEnv, cfg.AgentSocket = "", "/run/explicit.sock"
	got, err := transportConfig(cfg, func(string) (string, bool) { t.Fatal("environment read"); return "", false }, noFile)
	if err != nil || got.AgentSocket != "/run/explicit.sock" {
		t.Fatalf("got %v %v", got.AgentSocket, err)
	}
}

// A named env var that is unset or empty is an error naming both the key and the variable; it
// never falls back to SSH_AUTH_SOCK.
func TestUnsetAgentSocketEnvIsAnError(t *testing.T) {
	for _, lookup := range []func(string) (string, bool){noEnv, env(map[string]string{"CHORE_AGENT_SOCK": "", "SSH_AUTH_SOCK": "/trap"})} {
		_, err := transportConfig(homelabServerRunnerConfig(), lookup, noFile)
		if err == nil || !strings.Contains(err.Error(), "homelab:agentSocketEnv") || !strings.Contains(err.Error(), "CHORE_AGENT_SOCK") {
			t.Fatalf("got %v", err)
		}
	}
}

// The transport never reads ~/.ssh, so asking it to is refused by key rather than ignored.
func TestReadingHomeIsRefused(t *testing.T) {
	for key, set := range map[string]func(*remote.Config){
		"homelab:readSshConfig":   func(c *remote.Config) { c.ReadSSHConfig = true },
		"homelab:readDefaultKeys": func(c *remote.Config) { c.ReadDefaultKeys = true },
		"homelab:readKnownHosts":  func(c *remote.Config) { c.ReadKnownHosts = true },
	} {
		cfg := homelabServerRunnerConfig()
		set(&cfg)
		_, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/s"}), noFile)
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s: got %v", key, err)
		}
	}
}

// A host-keys file is read from the path configured, comments and blank lines skipped; a file that
// cannot be read is an error naming the key.
func TestHostKeysFile(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.HostKeys, cfg.HostKeysFile = nil, "hosts/homelab.known_hosts"
	read := func(p string) ([]byte, error) {
		if p != "hosts/homelab.known_hosts" {
			t.Fatalf("read %s", p)
		}
		return []byte("# pinned\n[127.0.0.1]:20022 ssh-ed25519 AAAAfile\n\n"), nil
	}
	got, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/s"}), read)
	if err != nil || got.HostKeys[len(got.HostKeys)-1] != "[127.0.0.1]:20022 ssh-ed25519 AAAAfile" {
		t.Fatalf("got %v %v", got.HostKeys, err)
	}
	_, err = transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/s"}), noFile)
	if err == nil || !strings.Contains(err.Error(), "homelab:hostKeysFile") {
		t.Fatalf("got %v", err)
	}
}

// Something that is not a host key is an error, not a pin quietly dropped.
func TestMalformedHostKeyIsAnError(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.HostKeys = []string{"nonsense"}
	_, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/s"}), noFile)
	if err == nil || !strings.Contains(err.Error(), "homelab:hostKeys") {
		t.Fatalf("got %v", err)
	}
}

// A jump with no pins is passed on unpinned, for the transport's strict check to refuse.
func TestUnpinnedJumpIsLeftToTheTransport(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.Jumps[0].HostKeys = nil
	got, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/s"}), noFile)
	if err != nil || len(got.HostKeys) != 1 {
		t.Fatalf("got %v %v", got.HostKeys, err)
	}
}

// The real factory satisfies the Runner interface the resources use.
var _ remote.Runner = (*transport.Transport)(nil)

func TestAgentSocketCanUseExplicitlyNamedSSHAuthSock(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.AgentSocketEnv = "SSH_AUTH_SOCK"
	got, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/other.sock", "SSH_AUTH_SOCK": "/selected.sock"}), noFile)
	if err != nil || got.AgentSocket != "/selected.sock" {
		t.Fatalf("agent socket: %q, %v", got.AgentSocket, err)
	}
}

func TestTransportConfigKeepsStructuredJumpFields(t *testing.T) {
	cfg := homelabServerRunnerConfig()
	cfg.Jumps = []remote.Jump{
		{Address: "2001:db8::22", Port: 2222, User: "ipv6", HostKeys: []string{"  ssh-ed25519 AAAAipv6  "}},
		{Address: "relay.example:label", Port: 22, User: "admin", HostKeys: []string{"ssh-ed25519 AAAArelay"}},
	}
	got, err := transportConfig(cfg, env(map[string]string{"CHORE_AGENT_SOCK": "/agent.sock"}), noFile)
	if err != nil {
		t.Fatal(err)
	}
	wantHops := []transport.Hop{
		{Address: "2001:db8::22", Port: 2222, User: "ipv6"},
		{Address: "relay.example:label", Port: 22, User: "admin"},
		{Address: "127.0.0.1", Port: 20022, User: "chris"},
	}
	wantPins := []string{
		"[2001:db8::22]:2222 ssh-ed25519 AAAAipv6",
		"relay.example:label ssh-ed25519 AAAArelay",
		"[127.0.0.1]:20022 ssh-ed25519 AAAAhost",
	}
	if !reflect.DeepEqual(got.Hops, wantHops) || !reflect.DeepEqual(got.HostKeys, wantPins) {
		t.Fatalf("route or pins changed: %+v", got)
	}
}
