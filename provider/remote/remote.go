// Package remote is the one way a resource reaches the machine it manages: run a shell command,
// optionally with stdin, and get back what happened. The ssh transport (provider/transport, #34)
// implements it; tests use the scripted fake in remotetest.
package remote

import "context"

// Runner runs a shell command on the managed machine. A non-zero exit is a result, not an error; err means the transport failed.
type Runner interface {
	Run(ctx context.Context, command string, stdin []byte) (stdout, stderr []byte, code int, err error)
}

// Jump is one hop on the way to the machine.
type Jump struct {
	Address  string
	Port     int
	User     string
	HostKeys []string
}

// Config is everything a transport is given, exactly as the project configured it. Nothing in it
// has been defaulted, resolved from the environment or read from the home directory: resolving
// AgentSocketEnv, reading HostKeysFile and honouring the Read* switches is the transport's job,
// and it must not fall back from one source to another.
type Config struct {
	Address string
	Port    int
	User    string
	Jumps   []Jump

	// Exactly one of these is set.
	AgentSocket    string
	AgentSocketEnv string

	// Identity is an optional explicit private key (a secret), empty when none was configured.
	Identity string

	// Exactly one of these is set.
	HostKeys     []string
	HostKeysFile string

	ReadSSHConfig         bool
	ReadDefaultKeys       bool
	ReadKnownHosts        bool
	StrictHostKeyChecking bool

	// Escalation is "sudo", "none" or "sudo-password"; SudoPassword is set only for the last.
	Escalation   string
	SudoPassword string
}

// Factory builds the Runner for a configured provider.
type Factory func(ctx context.Context, cfg Config) (Runner, error)
