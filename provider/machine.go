package homelab

import (
	"context"
	"fmt"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// machine is the configured target: the runner plus the escalation policy, mirroring src/ssh.ts
// ask, must and escalate.
type machine struct {
	runner remote.Runner
	cfg    remote.Config
}

type ran struct {
	code int
	out  string
	err  string
}

// ask runs a command and hands back what happened, including a non-zero exit. With
// escalation "sudo-password" the password goes down stdin for sudo -S, as askOver does.
func (m *machine) ask(ctx context.Context, command string) (ran, error) {
	var stdin []byte
	if m.cfg.Escalation == "sudo-password" {
		stdin = []byte(m.cfg.SudoPassword + "\n")
	}
	out, errb, code, err := m.runner.Run(ctx, command, stdin)
	if err != nil {
		return ran{}, err
	}
	return ran{code: code, out: string(out), err: string(errb)}, nil
}

// must runs a command and fails on a non-zero exit.
func (m *machine) must(ctx context.Context, command string) (string, error) {
	r, err := m.ask(ctx, command)
	if err != nil {
		return "", err
	}
	if r.code != 0 {
		detail := strings.TrimSpace(r.err)
		if detail == "" {
			detail = strings.TrimSpace(r.out)
		}
		return "", fmt.Errorf("`%s` failed on %s (exit %d): %s", command, m.describe(), r.code, detail)
	}
	return r.out, nil
}

// describe names the machine and the route to it, as src/ssh.ts describe does: the same machine
// reached two ways fails differently, and "cannot reach chris@127.0.0.1:20022" without the jump
// names a destination nobody recognises. A port is shown when it is not 22, and jumps follow "via"
// in order, as a ProxyJump chain is written.
func (m *machine) describe() string {
	where := hostPort(m.cfg.User, m.cfg.Address, m.cfg.Port)
	if len(m.cfg.Jumps) == 0 {
		return where
	}
	hops := make([]string, len(m.cfg.Jumps))
	for i, j := range m.cfg.Jumps {
		hops[i] = hostPort(j.User, j.Address, j.Port)
	}
	return where + " via " + strings.Join(hops, ",")
}

func hostPort(user, address string, port int) string {
	if port != 0 && port != 22 {
		return fmt.Sprintf("%s@%s:%d", user, address, port)
	}
	return user + "@" + address
}

// escalate wraps a command so it runs as root, by the configured means (src/ssh.ts escalateOn).
func (m *machine) escalate(command string) string {
	return escalate(m.cfg.Escalation, command)
}

func escalate(escalation, command string) string {
	switch escalation {
	case "none":
		return command
	case "sudo-password":
		// -S reads the password from stdin, which ask supplies; -p '' stops sudo writing a prompt
		// into stderr where it would end up quoted back in an error message
		return "sudo -S -p '' sh -c " + shellQuote(command)
	default:
		return asRoot(command)
	}
}

// asRoot is src/ssh.ts asRoot: sudo -n, which never prompts.
func asRoot(command string) string {
	return "sudo -n sh -c " + shellQuote(command)
}

// shellQuote quotes a string so a shell treats it as one argument, whatever is in it.
func shellQuote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}
