package homelab

import (
	"context"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/cmdline"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// KernelCmdline is parameters on the kernel command line the firmware boots with. Ported from
// src/resources/cmdline.ts. Parameters are merged into the line, never removed, and delete leaves
// the line alone: taking a parameter out could stop the machine booting.
type KernelCmdline struct{}

// KernelCmdlineArgs are the inputs of a KernelCmdline.
type KernelCmdlineArgs struct {
	Flags []string `pulumi:"flags"`
	Path  *string  `pulumi:"path,optional"`
}

// KernelCmdlineState is what is kept in state.
type KernelCmdlineState struct {
	Flags   []string `pulumi:"flags"`
	Path    string   `pulumi:"path"`
	Cmdline string   `pulumi:"cmdline"`
}

func (a *KernelCmdlineArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Flags, "Parameters that must be on the line, as key=value or bare.")
	an.Describe(&a.Path, "The file, when it is not /boot/firmware/cmdline.txt or /boot/cmdline.txt.")
}

func (s *KernelCmdlineState) Annotate(an infer.Annotator) {
	an.Describe(&s.Cmdline, "The whole line as the file has it.")
}

func (r *KernelCmdline) Annotate(a infer.Annotator) {
	a.Describe(&r, "Parameters on the kernel command line. Takes effect at the next boot.")
	a.SetToken("index", "KernelCmdline")
}

var cmdlineCandidates = []string{"/boot/firmware/cmdline.txt", "/boot/cmdline.txt"}

func cmdlineReadCommand(path string) string {
	candidates := cmdlineCandidates
	if path != "" {
		candidates = []string{path}
	}
	tests := make([]string, len(candidates))
	for i, c := range candidates {
		q := shellQuote(c)
		tests[i] = "test -f " + q + " && { echo " + q + "; cat " + q + "; exit 0; }"
	}
	return strings.Join(tests, "; ") + "; exit 9"
}

type cmdlineFound struct{ path, cmdline string }

func readCmdline(ctx context.Context, m *machine, path string) (*cmdlineFound, error) {
	asked, err := m.ask(ctx, m.escalate(cmdlineReadCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read the kernel command line file: %s", jscompat.Trim(asked.err))
	}
	head, rest := firstLineSplit(asked.out)
	return &cmdlineFound{path: jscompat.Trim(head), cmdline: jscompat.Trim(rest)}, nil
}

func applyCmdline(ctx context.Context, m *machine, a KernelCmdlineArgs) (KernelCmdlineState, error) {
	found, err := readCmdline(ctx, m, deref(a.Path))
	if err != nil {
		return KernelCmdlineState{}, err
	}
	if found == nil {
		looked := strings.Join(cmdlineCandidates, " or ")
		if a.Path != nil {
			looked = *a.Path
		}
		return KernelCmdlineState{}, fmt.Errorf("no kernel command line file at %s on %s: this is not an image that boots that way", looked, m.describe())
	}
	line := cmdline.Merge(found.cmdline, a.Flags)
	if line != found.cmdline {
		if _, err := m.must(ctx, m.escalate(heredoc(found.path, line))); err != nil {
			return KernelCmdlineState{}, err
		}
	}
	return KernelCmdlineState{Flags: a.Flags, Path: found.path, Cmdline: line}, nil
}

// Create merges the parameters in; the id is the file.
func (KernelCmdline) Create(ctx context.Context, req infer.CreateRequest[KernelCmdlineArgs]) (infer.CreateResponse[KernelCmdlineState], error) {
	if req.DryRun {
		return infer.CreateResponse[KernelCmdlineState]{ID: deref(req.Inputs.Path), Output: KernelCmdlineState{Flags: req.Inputs.Flags, Path: deref(req.Inputs.Path)}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[KernelCmdlineState]{}, err
	}
	state, err := applyCmdline(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[KernelCmdlineState]{}, err
	}
	return infer.CreateResponse[KernelCmdlineState]{ID: state.Path, Output: state}, nil
}

// Read reports the line.
func (KernelCmdline) Read(ctx context.Context, req infer.ReadRequest[KernelCmdlineArgs, KernelCmdlineState]) (infer.ReadResponse[KernelCmdlineArgs, KernelCmdlineState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[KernelCmdlineArgs, KernelCmdlineState]{}, err
	}
	found, err := readCmdline(ctx, m, req.ID)
	if err != nil || found == nil {
		return infer.ReadResponse[KernelCmdlineArgs, KernelCmdlineState]{}, err
	}
	flags := req.State.Flags
	if flags == nil {
		flags = []string{}
	}
	inputs := req.Inputs
	if inputs.Flags == nil {
		inputs = KernelCmdlineArgs{Flags: flags, Path: &found.path}
	}
	return infer.ReadResponse[KernelCmdlineArgs, KernelCmdlineState]{ID: req.ID, Inputs: inputs,
		State: KernelCmdlineState{Flags: flags, Path: found.path, Cmdline: found.cmdline}}, nil
}

// Update merges again, into the file named by the id unless a path is given.
func (KernelCmdline) Update(ctx context.Context, req infer.UpdateRequest[KernelCmdlineArgs, KernelCmdlineState]) (infer.UpdateResponse[KernelCmdlineState], error) {
	a := req.Inputs
	if a.Path == nil {
		id := req.ID
		a.Path = &id
	}
	if req.DryRun {
		return infer.UpdateResponse[KernelCmdlineState]{Output: KernelCmdlineState{Flags: a.Flags, Path: *a.Path, Cmdline: cmdline.Merge(req.State.Cmdline, a.Flags)}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[KernelCmdlineState]{}, err
	}
	state, err := applyCmdline(ctx, m, a)
	return infer.UpdateResponse[KernelCmdlineState]{Output: state}, err
}

// Diff is whether merging the flags into the line as it stands would change it. Never a
// replacement.
func (KernelCmdline) Diff(_ context.Context, req infer.DiffRequest[KernelCmdlineArgs, KernelCmdlineState]) (p.DiffResponse, error) {
	diff := diffOf(cmdline.Merge(req.State.Cmdline, req.Inputs.Flags) != req.State.Cmdline, false)
	diff.DetailedDiff["path"] = p.PropertyDiff{Kind: p.Stable}
	return diff, nil
}

// Delete leaves the line as it is: a parameter taken out could stop the machine booting, and the
// stack ceasing to care is not a reason to change the boot.
func (KernelCmdline) Delete(context.Context, infer.DeleteRequest[KernelCmdlineState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
