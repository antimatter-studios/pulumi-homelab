// Package remotetest is a scripted fake remote.Runner for tests.
package remotetest

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Call is one command the fake was asked to run.
type Call struct {
	Command string
	Stdin   string
}

// Reply is what the fake answers to a command.
type Reply struct {
	Stdout string
	Stderr string
	Code   int
	Err    error
}

// Step answers commands that satisfy Match, Times times (0 means for ever).
type Step struct {
	Match func(command string) bool
	Reply func(command string) Reply
	Times int
	used  int
}

// Runner is a scripted remote.Runner. Steps are tried in order; the first whose Match accepts the
// command and has uses left answers it. A command nothing matches fails the test via an error, so
// an unexpected command is never silently answered.
type Runner struct {
	mu    sync.Mutex
	steps []*Step
	Calls []Call
}

// New returns an empty scripted runner.
func New() *Runner { return &Runner{} }

// On answers commands equal to command with reply, for ever.
func (r *Runner) On(command string, reply Reply) *Runner {
	return r.When(func(c string) bool { return c == command }, func(string) Reply { return reply }, 0)
}

// Once answers the next command equal to command with reply, once.
func (r *Runner) Once(command string, reply Reply) *Runner {
	return r.When(func(c string) bool { return c == command }, func(string) Reply { return reply }, 1)
}

// When adds a general step.
func (r *Runner) When(match func(string) bool, reply func(string) Reply, times int) *Runner {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, &Step{Match: match, Reply: reply, Times: times})
	return r
}

// Run implements remote.Runner.
func (r *Runner) Run(_ context.Context, command string, stdin []byte) ([]byte, []byte, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, Call{Command: command, Stdin: string(stdin)})
	for _, s := range r.steps {
		if s.Times != 0 && s.used >= s.Times {
			continue
		}
		if !s.Match(command) {
			continue
		}
		s.used++
		rep := s.Reply(command)
		return []byte(rep.Stdout), []byte(rep.Stderr), rep.Code, rep.Err
	}
	return nil, nil, 0, fmt.Errorf("remotetest: unscripted command %q", command)
}

// Commands lists the commands run so far, in order.
func (r *Runner) Commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.Calls))
	for i, c := range r.Calls {
		out[i] = c.Command
	}
	return out
}

// Ran reports whether any command run so far contains part.
func (r *Runner) Ran(part string) bool {
	for _, c := range r.Commands() {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}
