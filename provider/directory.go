package homelab

import (
	"context"
	"errors"
	"fmt"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
	"github.com/antimatter-studios/pulumi-homelab/provider/stat"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Directory is a directory that should exist, with its mode and ownership. Ported from
// src/resources/directory.ts.
//
// Creating makes parents; deleting removes only the leaf, and only when it is empty (rmdir, never
// rm -rf). setgid and sticky are flags folded into the mode's leading digit. There is no setuid
// field: the bit means nothing on a Linux directory, though a four-digit mode carrying it is still
// accepted, so a directory that has it can be adopted as it is.
type Directory struct{}

// DirectoryArgs are the inputs of a Directory.
type DirectoryArgs struct {
	Path   string  `pulumi:"path"`
	Mode   *string `pulumi:"mode,optional"`
	Owner  *string `pulumi:"owner,optional"`
	Group  *string `pulumi:"group,optional"`
	Setgid *bool   `pulumi:"setgid,optional"`
	Sticky *bool   `pulumi:"sticky,optional"`
}

// DirectoryState is what is kept in state: the four-digit mode, special bits and all, in the
// shape stat answers in.
type DirectoryState struct {
	Path   string `pulumi:"path"`
	Mode   string `pulumi:"mode"`
	Owner  string `pulumi:"owner"`
	Group  string `pulumi:"group"`
	Setgid *bool  `pulumi:"setgid,optional"`
	Sticky *bool  `pulumi:"sticky,optional"`
}

const (
	directoryDefaultMode  = "0755"
	directoryDefaultOwner = "root"
	directoryDefaultGroup = "root"
)

func (d *DirectoryArgs) Annotate(a infer.Annotator) {
	a.Describe(&d.Path, "The directory. Its parents are made as needed and never removed.")
	a.Describe(&d.Mode, "Octal, as it is written everywhere else: '0755'. Four digits are accepted and mean what they always "+
		"have; a non-zero leading digit beside setgid or sticky is refused, because that is two answers to one question.")
	a.SetDefault(&d.Mode, directoryDefaultMode)
	a.Describe(&d.Owner, "The owning user.")
	a.SetDefault(&d.Owner, directoryDefaultOwner)
	a.Describe(&d.Group, "The owning group.")
	a.SetDefault(&d.Group, directoryDefaultGroup)
	a.Describe(&d.Setgid, "New entries inherit this directory's group rather than their creator's: what makes a shared area shared.")
	a.Describe(&d.Sticky, "Only an entry's owner may remove it. The bit that belongs beside any write grant on a shared directory.")
}

func (d *DirectoryState) Annotate(a infer.Annotator) {
	a.Describe(&d.Mode, "The four-digit mode, special bits and all: the shape stat answers in.")
}

func (d *Directory) Annotate(a infer.Annotator) {
	a.Describe(&d, "A directory the machine should have, checked against stat rather than remembered.")
	a.SetToken("index", "Directory")
}

func directoryFlags(setgid, sticky *bool) mode.Flags {
	// no setuid: it means nothing on a Linux directory, so there is no field to fold in
	return mode.Flags{Setgid: setgid, Sticky: sticky}
}

// directoryModeRefusal is the check the declaration gets: a mode that is not a mode, or one whose
// leading digit and the flags beside it both answer the same question.
func directoryModeRefusal(args DirectoryArgs) string {
	m := directoryDefaultMode
	if args.Mode != nil {
		m = *args.Mode
	}
	if bad := mode.ModeRefusal(m); bad != "" {
		return bad
	}
	return mode.SpecialBitsRefusal(m, directoryFlags(args.Setgid, args.Sticky))
}

// resolveMode is the mode a declaration settles to, flags folded into the leading digit: the one
// string that is written, compared and read back.
func resolveMode(args DirectoryArgs) (string, error) {
	if bad := directoryModeRefusal(args); bad != "" {
		return "", errors.New(bad)
	}
	m := directoryDefaultMode
	if args.Mode != nil {
		m = *args.Mode
	}
	return mode.WithSpecialBits(m, directoryFlags(args.Setgid, args.Sticky)), nil
}

// wantedDirectory is { ...DEFAULTS, ...args, mode: resolveMode(args) }.
func wantedDirectory(args DirectoryArgs) (DirectoryState, error) {
	resolved, err := resolveMode(args)
	if err != nil {
		return DirectoryState{}, err
	}
	owner, group := directoryDefaultOwner, directoryDefaultGroup
	if args.Owner != nil {
		owner = *args.Owner
	}
	if args.Group != nil {
		group = *args.Group
	}
	return DirectoryState{Path: args.Path, Mode: resolved, Owner: owner, Group: group, Setgid: args.Setgid, Sticky: args.Sticky}, nil
}

// readDirectory is what the machine says is at that path, or nil where there is nothing. Something
// that is not a directory is an error naming what it is.
func readDirectory(ctx context.Context, m *machine, path string) (*DirectoryState, error) {
	quoted := shellQuote(path)
	// -e is false for a broken symlink, which is still something in the way
	asked, err := m.ask(ctx, m.escalate(
		"{ test -e "+quoted+" || test -L "+quoted+"; } || exit 9; "+
			"stat -c '%F|%a|%U|%G' "+quoted))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	found := stat.ParseStat(asked.out)
	if found.Kind != "directory" {
		kind := found.Kind
		if kind == "" {
			kind = "something else"
		}
		return nil, fmt.Errorf("%s is a %s on %s, not a directory: refusing to guess what to do with it", path, kind, m.describe())
	}
	return &DirectoryState{Path: path, Mode: found.Mode, Owner: found.Owner, Group: found.Group}, nil
}

// applyDirectory makes it and makes it match. mkdir -p is also what makes this safe to run again.
func applyDirectory(ctx context.Context, m *machine, d DirectoryState) error {
	path := shellQuote(d.Path)
	ownership := shellQuote(d.Owner + ":" + d.Group)
	_, err := m.must(ctx, m.escalate("mkdir -p "+path+" && chmod "+d.Mode+" "+path+" && chown "+ownership+" "+path))
	return err
}

// Check refuses a mode that is not a mode, or a mode and a flag that answer the same question, at
// preview: the declaration is wrong rather than the machine.
func (Directory) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[DirectoryArgs], error) {
	args, failures, err := infer.DefaultCheck[DirectoryArgs](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[DirectoryArgs]{Inputs: args, Failures: failures}, err
	}
	if bad := directoryModeRefusal(args); bad != "" {
		failures = append(failures, p.CheckFailure{Property: "mode", Reason: bad})
	}
	return infer.CheckResponse[DirectoryArgs]{Inputs: args, Failures: failures}, nil
}

// Create makes the directory, with its parents, and settles its mode and ownership.
func (Directory) Create(ctx context.Context, req infer.CreateRequest[DirectoryArgs]) (infer.CreateResponse[DirectoryState], error) {
	wanted, err := wantedDirectory(req.Inputs)
	if err != nil {
		return infer.CreateResponse[DirectoryState]{}, err
	}
	if req.DryRun {
		return infer.CreateResponse[DirectoryState]{ID: req.Inputs.Path, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[DirectoryState]{}, err
	}
	if err := applyDirectory(ctx, m, wanted); err != nil {
		return infer.CreateResponse[DirectoryState]{}, err
	}
	return infer.CreateResponse[DirectoryState]{ID: req.Inputs.Path, Output: wanted}, nil
}

// Read reports what stat says; a directory gone from the machine is forgotten, and the next up puts
// it back. On import, the inputs are what the machine says.
func (Directory) Read(ctx context.Context, req infer.ReadRequest[DirectoryArgs, DirectoryState]) (infer.ReadResponse[DirectoryArgs, DirectoryState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[DirectoryArgs, DirectoryState]{}, err
	}
	actual, err := readDirectory(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[DirectoryArgs, DirectoryState]{}, err
	}
	if actual == nil {
		return infer.ReadResponse[DirectoryArgs, DirectoryState]{}, nil
	}
	state := req.State
	state.Path, state.Mode, state.Owner, state.Group = actual.Path, actual.Mode, actual.Owner, actual.Group
	inputs := req.Inputs
	if inputs.Path == "" {
		inputs = DirectoryArgs{Path: actual.Path, Mode: &actual.Mode, Owner: &actual.Owner, Group: &actual.Group}
	}
	return infer.ReadResponse[DirectoryArgs, DirectoryState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the directory under its id, touching it only when stat says it differs.
func (Directory) Update(ctx context.Context, req infer.UpdateRequest[DirectoryArgs, DirectoryState]) (infer.UpdateResponse[DirectoryState], error) {
	args := req.Inputs
	args.Path = req.ID
	wanted, err := wantedDirectory(args)
	if err != nil {
		return infer.UpdateResponse[DirectoryState]{}, err
	}
	if req.DryRun {
		return infer.UpdateResponse[DirectoryState]{Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[DirectoryState]{}, err
	}
	current, err := readDirectory(ctx, m, req.ID)
	if err != nil {
		return infer.UpdateResponse[DirectoryState]{}, err
	}
	same := current != nil && current.Mode == wanted.Mode && current.Owner == wanted.Owner && current.Group == wanted.Group
	if !same {
		if err := applyDirectory(ctx, m, wanted); err != nil {
			return infer.UpdateResponse[DirectoryState]{}, err
		}
	}
	return infer.UpdateResponse[DirectoryState]{Output: wanted}, nil
}

// Diff: mode, owner and group are updates; a new path is a new directory, created before the old
// one is removed.
func (Directory) Diff(_ context.Context, req infer.DiffRequest[DirectoryArgs, DirectoryState]) (p.DiffResponse, error) {
	wanted, err := wantedDirectory(req.Inputs)
	if err != nil {
		return p.DiffResponse{}, err
	}
	resp := p.DiffResponse{DeleteBeforeReplace: false, DetailedDiff: map[string]p.PropertyDiff{}}
	if req.State.Mode != wanted.Mode {
		resp.DetailedDiff["mode"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Owner != wanted.Owner {
		resp.DetailedDiff["owner"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Group != wanted.Group {
		resp.DetailedDiff["group"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Path != wanted.Path {
		resp.DetailedDiff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete is rmdir without -p: only the leaf, and only when it is empty. Anything still in there is
// the machine saying the code's picture of it is incomplete, which is worth a failed deployment.
func (Directory) Delete(ctx context.Context, req infer.DeleteRequest[DirectoryState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate("rmdir "+shellQuote(req.ID)))
	return infer.DeleteResponse{}, err
}
