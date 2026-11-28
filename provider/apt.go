package homelab

import (
	"context"
	"fmt"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/apt"
	"github.com/antimatter-studios/pulumi-homelab/provider/aptmark"
)

// AptPackage is a package that should be installed. Ported from src/resources/apt.ts.
//
// Deliberately not a wrapper around `apt-get install`: this asks dpkg what is actually installed,
// so removing a package by hand shows up as drift on the next refresh instead of being invisible
// for ever. It manages presence and nothing else; pinning a version is left out on purpose, because
// on a machine that runs `apt upgrade` on its own schedule a pinned version turns every security
// update into drift.
type AptPackage struct{}

// AptPackageArgs are the inputs of an AptPackage.
type AptPackageArgs struct {
	Name   string `pulumi:"name"`
	Update *bool  `pulumi:"update,optional"`
}

// AptPackageState is what is kept in state.
type AptPackageState struct {
	Name    string `pulumi:"name"`
	Update  bool   `pulumi:"update"`
	Version string `pulumi:"version"`
	Manual  bool   `pulumi:"manual"`
}

func (a *AptPackageArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "What apt calls it.")
	an.Describe(&a.Update, "Whether to refresh the package lists before installing. Off by default because it is slow and shared.")
}

func (s *AptPackageState) Annotate(an infer.Annotator) {
	an.Describe(&s.Version, "What is actually installed, which is how a version bump shows up in a diff without being asked for.")
	an.Describe(&s.Manual, "Whether apt holds it as manually installed rather than as somebody else's dependency.")
}

func (r *AptPackage) Annotate(an infer.Annotator) {
	an.Describe(&r, "A package the machine should have, checked against dpkg rather than remembered.")
	an.SetToken("index", "AptPackage")
}

// readPackage is what dpkg says about it: the version, or ok false when it is not installed.
func readPackage(ctx context.Context, m *machine, name string) (version string, ok bool, err error) {
	// dpkg-query -W returns 1 for missing packages and 2 for fatal errors.
	asked, err := m.commandResult(ctx, "dpkg-query", "dpkg-query -W -f='${db:Status-Status} ${Version}' -- "+shellQuote(name), 1)
	if err != nil || asked.code == 1 {
		return "", false, err
	}
	version, ok = apt.ParseDpkgStatus(asked.out)
	return version, ok, nil
}

// readManual is whether apt holds this one as manually installed.
func readManual(ctx context.Context, m *machine, name string) (bool, error) {
	asked, err := m.commandResult(ctx, "apt-mark showmanual", "apt-mark showmanual -- "+shellQuote(name))
	if err != nil {
		return false, err
	}
	return slices.Contains(aptmark.ParseManual(asked.out), strings.Split(name, ":")[0]), nil
}

func installAptPackage(ctx context.Context, m *machine, args AptPackageArgs) (string, error) {
	refresh := ""
	if deref(args.Update) {
		refresh = aptGet + " update -qq && "
	}
	// noninteractive or a package with a config prompt hangs the deployment for ever behind a
	// dialogue nobody can see, let alone answer
	if _, err := m.must(ctx, m.escalate(refresh+"DEBIAN_FRONTEND=noninteractive "+aptGet+" install -y -qq "+shellQuote(args.Name))); err != nil {
		return "", err
	}
	version, ok, err := readPackage(ctx, m, args.Name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("apt said it installed %s, but dpkg cannot find it", args.Name)
	}
	return version, nil
}

// markManual makes apt hold it as asked for rather than as somebody else's dependency.
func markManual(ctx context.Context, m *machine, name string) (bool, error) {
	if manual, err := readManual(ctx, m, name); err != nil || manual {
		return manual, err
	}
	if mark, ok := aptmark.MarkManualCommand([]string{name}); ok {
		if _, err := m.must(ctx, m.escalate(mark)); err != nil {
			return false, err
		}
	}
	return readManual(ctx, m, name)
}

// Create installs it, and marks it manual.
func (AptPackage) Create(ctx context.Context, req infer.CreateRequest[AptPackageArgs]) (infer.CreateResponse[AptPackageState], error) {
	args := req.Inputs
	if req.DryRun {
		return infer.CreateResponse[AptPackageState]{ID: args.Name, Output: AptPackageState{Name: args.Name, Update: deref(args.Update)}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[AptPackageState]{}, err
	}
	version, err := installAptPackage(ctx, m, args)
	if err != nil {
		return infer.CreateResponse[AptPackageState]{}, err
	}
	manual, err := markManual(ctx, m, args.Name)
	if err != nil {
		return infer.CreateResponse[AptPackageState]{}, err
	}
	return infer.CreateResponse[AptPackageState]{ID: args.Name, Output: AptPackageState{
		Name: args.Name, Update: deref(args.Update), Version: version, Manual: manual,
	}}, nil
}

// Read asks dpkg; somebody removed it by hand and Pulumi forgets it, so the next up puts it back.
// An import has no state, so every field stands on its own.
func (AptPackage) Read(ctx context.Context, req infer.ReadRequest[AptPackageArgs, AptPackageState]) (infer.ReadResponse[AptPackageArgs, AptPackageState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[AptPackageArgs, AptPackageState]{}, err
	}
	version, ok, err := readPackage(ctx, m, req.ID)
	if err != nil || !ok {
		return infer.ReadResponse[AptPackageArgs, AptPackageState]{}, err
	}
	// from the machine rather than from what was remembered: an `apt-mark auto` somebody ran, or a
	// package this adopted without ever installing, both show up only by asking
	manual, err := readManual(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[AptPackageArgs, AptPackageState]{}, err
	}
	state := req.State
	if req.Inputs.Update != nil {
		state.Update = *req.Inputs.Update
	}
	state.Name, state.Version, state.Manual = req.ID, version, manual
	inputs := req.Inputs
	inputs.Name = req.ID
	return infer.ReadResponse[AptPackageArgs, AptPackageState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update: the only thing that can change in place is whether the lists are refreshed first; the
// name is the identity and a different name is a different package.
func (AptPackage) Update(ctx context.Context, req infer.UpdateRequest[AptPackageArgs, AptPackageState]) (infer.UpdateResponse[AptPackageState], error) {
	args := req.Inputs
	args.Name = req.ID
	out := req.State
	out.Update = deref(args.Update)
	if req.DryRun {
		return infer.UpdateResponse[AptPackageState]{Output: out}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[AptPackageState]{}, err
	}
	version, ok, err := readPackage(ctx, m, req.ID)
	if err != nil {
		return infer.UpdateResponse[AptPackageState]{}, err
	}
	if !ok {
		if version, err = installAptPackage(ctx, m, args); err != nil {
			return infer.UpdateResponse[AptPackageState]{}, err
		}
	}
	manual, err := markManual(ctx, m, req.ID)
	if err != nil {
		return infer.UpdateResponse[AptPackageState]{}, err
	}
	out.Version, out.Manual = version, manual
	return infer.UpdateResponse[AptPackageState]{Output: out}, nil
}

// Diff: auto-marked is present and still removable by an autoremove aimed at something else, so the
// declaration is weaker than it reads until it is fixed. A different name replaces.
func (AptPackage) Diff(_ context.Context, req infer.DiffRequest[AptPackageArgs, AptPackageState]) (p.DiffResponse, error) {
	nameChanged := req.State.Name != req.Inputs.Name
	updateChanged := req.State.Update != deref(req.Inputs.Update)
	resp := p.DiffResponse{
		HasChanges:   !req.State.Manual || nameChanged || updateChanged,
		DetailedDiff: map[string]p.PropertyDiff{},
	}
	if nameChanged {
		resp.DetailedDiff["name"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if updateChanged {
		resp.DetailedDiff["update"] = p.PropertyDiff{Kind: p.Update}
	}
	if !req.State.Manual {
		resp.DetailedDiff["manual"] = p.PropertyDiff{Kind: p.Update}
	}
	return resp, nil
}

// Delete purges it. Autoremove is deliberately not run.
func (AptPackage) Delete(ctx context.Context, req infer.DeleteRequest[AptPackageState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(purgeCommand(req.ID)))
	return infer.DeleteResponse{}, err
}
