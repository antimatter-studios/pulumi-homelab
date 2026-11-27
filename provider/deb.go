package homelab

import (
	"context"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// DebPackage is a Debian package installed from a URL rather than from a repository, verified
// against a required SHA-256 and installed as a package dpkg knows about. Ported from
// src/resources/deb.ts.
type DebPackage struct{}

// DebPackageArgs are the inputs of a DebPackage.
type DebPackageArgs struct {
	Name   string `pulumi:"name"`
	URL    string `pulumi:"url"`
	Sha256 string `pulumi:"sha256"`
}

// DebPackageState is what is kept in state.
type DebPackageState struct {
	Name    string `pulumi:"name"`
	URL     string `pulumi:"url"`
	Sha256  string `pulumi:"sha256"`
	Version string `pulumi:"version"`
}

func (a *DebPackageArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The name dpkg knows it by, which is not necessarily the filename.")
	an.Describe(&a.URL, "Where to fetch the .deb from.")
	an.Describe(&a.Sha256, "SHA-256 of the file, and not optional: a checksum nobody set is a checksum nobody checked.")
}

func (s *DebPackageState) Annotate(an infer.Annotator) {
	an.Describe(&s.Version, "What dpkg reports after the install, so a version change is visible in a diff.")
}

func (r *DebPackage) Annotate(an infer.Annotator) {
	an.Describe(&r, "A .deb fetched from a URL, verified, and installed as a package dpkg knows about.")
	an.SetToken("index", "DebPackage")
}

// debInstallScript is the install as one shell command. The order is the whole substance: download
// into a private directory, verify before installing (set -e stops a mismatch there), install
// through apt-get rather than dpkg -i so dependencies resolve, and clean up through a trap whether or
// not the file was accepted.
func debInstallScript(url, sha256 string) string {
	return strings.Join([]string{
		"set -e",
		"dir=$(mktemp -d)",
		`trap "rm -rf \"$dir\"" EXIT`,
		`curl -fsSL --retry 3 -o "$dir/pkg.deb" ` + shellQuote(url),
		"echo " + shellQuote(sha256) + `"  $dir/pkg.deb" | sha256sum -c - >/dev/null`,
		// noninteractive for the same reason as AptPackage: a config prompt nobody can see is a
		// deployment that hangs for ever
		"DEBIAN_FRONTEND=noninteractive " + aptGet + ` install -y -qq "$dir/pkg.deb"`,
	}, "; ")
}

func installDeb(ctx context.Context, m *machine, args DebPackageArgs) (string, error) {
	if _, err := m.must(ctx, m.escalate(debInstallScript(args.URL, args.Sha256))); err != nil {
		return "", err
	}
	version, ok, err := readPackage(ctx, m, args.Name)
	if err != nil {
		return "", err
	}
	if !ok {
		// apt reported success and dpkg has never heard of it: the .deb provides a package under a
		// different name than the one declared
		return "", fmt.Errorf("installed %s but dpkg has no package named %s — check the name against 'dpkg-deb -f <file> Package'", args.URL, args.Name)
	}
	return version, nil
}

// Create installs it.
func (DebPackage) Create(ctx context.Context, req infer.CreateRequest[DebPackageArgs]) (infer.CreateResponse[DebPackageState], error) {
	args := req.Inputs
	out := DebPackageState{Name: args.Name, URL: args.URL, Sha256: args.Sha256}
	if req.DryRun {
		return infer.CreateResponse[DebPackageState]{ID: args.Name, Output: out}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[DebPackageState]{}, err
	}
	if out.Version, err = installDeb(ctx, m, args); err != nil {
		return infer.CreateResponse[DebPackageState]{}, err
	}
	return infer.CreateResponse[DebPackageState]{ID: args.Name, Output: out}, nil
}

// Read asks dpkg. Removed by hand: Pulumi forgets it, and the next up puts it back.
func (DebPackage) Read(ctx context.Context, req infer.ReadRequest[DebPackageArgs, DebPackageState]) (infer.ReadResponse[DebPackageArgs, DebPackageState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[DebPackageArgs, DebPackageState]{}, err
	}
	version, ok, err := readPackage(ctx, m, req.ID)
	if err != nil || !ok {
		return infer.ReadResponse[DebPackageArgs, DebPackageState]{}, err
	}
	state := req.State
	if state.URL == "" {
		state.URL = req.Inputs.URL
	}
	if state.Sha256 == "" {
		state.Sha256 = req.Inputs.Sha256
	}
	state.Name, state.Version = req.ID, version
	inputs := req.Inputs
	if inputs.Name == "" {
		inputs = DebPackageArgs{Name: req.ID, URL: state.URL, Sha256: state.Sha256}
	}
	return infer.ReadResponse[DebPackageArgs, DebPackageState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update reinstalls only when the file actually differs or the package is actually missing: an
// update also happens when the provider itself changes, and that must not re-download anything.
func (DebPackage) Update(ctx context.Context, req infer.UpdateRequest[DebPackageArgs, DebPackageState]) (infer.UpdateResponse[DebPackageState], error) {
	args := req.Inputs
	args.Name = req.ID
	out := req.State
	out.Name, out.URL, out.Sha256 = req.ID, args.URL, args.Sha256
	if req.DryRun {
		return infer.UpdateResponse[DebPackageState]{Output: out}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[DebPackageState]{}, err
	}
	present := false
	if req.State.URL == args.URL && req.State.Sha256 == args.Sha256 {
		if out.Version, present, err = readPackage(ctx, m, req.ID); err != nil {
			return infer.UpdateResponse[DebPackageState]{}, err
		}
	}
	if !present {
		if out.Version, err = installDeb(ctx, m, args); err != nil {
			return infer.UpdateResponse[DebPackageState]{}, err
		}
	}
	return infer.UpdateResponse[DebPackageState]{Output: out}, nil
}

// Diff: the name is the identity and replaces; a new url or checksum is a different file.
func (DebPackage) Diff(_ context.Context, req infer.DiffRequest[DebPackageArgs, DebPackageState]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	if req.State.Name != req.Inputs.Name {
		resp.DetailedDiff["name"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	if req.State.URL != req.Inputs.URL {
		resp.DetailedDiff["url"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Sha256 != req.Inputs.Sha256 {
		resp.DetailedDiff["sha256"] = p.PropertyDiff{Kind: p.Update}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete purges it, matching AptPackage. Autoremove is deliberately not run.
func (DebPackage) Delete(ctx context.Context, req infer.DeleteRequest[DebPackageState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(purgeCommand(req.ID)))
	return infer.DeleteResponse{}, err
}
