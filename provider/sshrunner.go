package homelab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	"github.com/antimatter-studios/pulumi-homelab/provider/transport"
)

// sshRunner is the real factory: it turns the project's configuration into a transport.Config and
// nothing else. Every value comes from the configuration; the only lookups outside it are the ones
// the configuration names explicitly — the env var in agentSocketEnv and the file in hostKeysFile.
// There is no fallback from one source to another: a named source that is empty or unreadable is
// an error naming the config key.
func sshRunner(_ context.Context, cfg remote.Config) (remote.Runner, error) {
	tc, err := transportConfig(cfg, os.LookupEnv, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return transport.New(tc)
}

// transportConfig is sshRunner without the side effects, so every rule can be tested.
func transportConfig(
	cfg remote.Config,
	lookupEnv func(string) (string, bool),
	readFile func(string) ([]byte, error),
) (transport.Config, error) {
	// The transport reads nothing from the home directory, so a configuration that asks for it
	// cannot be honoured. Refusing is the only answer that is not a silent fallback.
	for key, on := range map[string]bool{
		"homelab:readSshConfig":   cfg.ReadSSHConfig,
		"homelab:readDefaultKeys": cfg.ReadDefaultKeys,
		"homelab:readKnownHosts":  cfg.ReadKnownHosts,
	} {
		if on {
			return transport.Config{}, fmt.Errorf("%s is true, but the ssh transport never reads ~/.ssh; set it to false and configure the source explicitly", key)
		}
	}

	socket := cfg.AgentSocket
	if cfg.AgentSocketEnv != "" {
		value, ok := lookupEnv(cfg.AgentSocketEnv)
		if !ok || value == "" {
			return transport.Config{}, fmt.Errorf("homelab:agentSocketEnv names %s, which is not set; start the agent (for this project, `chore agent`) before deploying", cfg.AgentSocketEnv)
		}
		socket = value
	}

	var hops []transport.Hop
	var keys []string
	for i, j := range cfg.Jumps {
		hops = append(hops, transport.Hop{Address: j.Address, Port: j.Port, User: j.User})
		if len(j.HostKeys) == 0 {
			// unpinned: the transport refuses it under strict checking, and that is its call
			continue
		}
		pinned, err := knownHostsLines(j.HostKeys, j.Address, j.Port)
		if err != nil {
			return transport.Config{}, fmt.Errorf("homelab:jumps[%d].hostKeys: %w", i, err)
		}
		keys = append(keys, pinned...)
	}
	hops = append(hops, transport.Hop{Address: cfg.Address, Port: cfg.Port, User: cfg.User})

	machineKeys := cfg.HostKeys
	if cfg.HostKeysFile != "" {
		data, err := readFile(cfg.HostKeysFile)
		if err != nil {
			return transport.Config{}, fmt.Errorf("homelab:hostKeysFile: %w", err)
		}
		machineKeys = strings.Split(string(data), "\n")
	}
	pinned, err := knownHostsLines(machineKeys, cfg.Address, cfg.Port)
	if err != nil {
		key := "homelab:hostKeys"
		if cfg.HostKeysFile != "" {
			key = "homelab:hostKeysFile"
		}
		return transport.Config{}, fmt.Errorf("%s: %w", key, err)
	}
	keys = append(keys, pinned...)

	tc := transport.Config{
		Hops:                  hops,
		AgentSocket:           socket,
		HostKeys:              keys,
		StrictHostKeyChecking: cfg.StrictHostKeyChecking,
	}
	if cfg.Identity != "" {
		tc.Identity = []byte(cfg.Identity)
	}
	return tc, nil
}

// knownHostsLines accepts pinned keys either as full known_hosts lines (`host type key`) or as a
// bare `type key`, which is pinned to the host it was configured for. Blank lines and comments are
// skipped. Anything else is an error rather than a key quietly ignored.
func knownHostsLines(lines []string, address string, port int) ([]string, error) {
	name := address
	if port != 0 && port != 22 {
		name = "[" + address + "]:" + strconv.Itoa(port)
	}
	var out []string
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch {
		case len(fields) >= 2 && (strings.HasPrefix(fields[0], "ssh-") || strings.HasPrefix(fields[0], "ecdsa-")):
			out = append(out, name+" "+line)
		case len(fields) >= 3:
			out = append(out, line)
		default:
			return nil, fmt.Errorf("line %d is not a host key: %q", i+1, line)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no host keys")
	}
	return out, nil
}
