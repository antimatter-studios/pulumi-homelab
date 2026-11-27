package homelab

import (
	"context"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// OfficialInstall is a tool installed by its vendor's own installer, into an account, and then
// left to update itself. Ported from src/resources/installer.ts.
//
// It declares only that the tool is installed, the vendor's way, as the account that uses it. If
// the executable is missing or does not run, the installer runs; if it is there at whatever version,
// that is success. The installer is fetched from the vendor and not checksummed, which is the trust
// the vendor's own instructions ask for.
type OfficialInstall struct{}

// OfficialInstallArgs are the inputs of an OfficialInstall.
type OfficialInstallArgs struct {
	Account   string  `pulumi:"account"`
	Home      *string `pulumi:"home"`
	Installer *string `pulumi:"installer"`
	Bin       string  `pulumi:"bin"`
}

// OfficialInstallState is what is kept in state.
type OfficialInstallState struct {
	Account   string  `pulumi:"account"`
	Home      *string `pulumi:"home,optional"`
	Installer *string `pulumi:"installer,optional"`
	Bin       string  `pulumi:"bin"`
	Version   string  `pulumi:"version"`
}

func installState(args OfficialInstallArgs, version string) OfficialInstallState {
	return OfficialInstallState{Account: args.Account, Home: args.Home, Installer: args.Installer, Bin: args.Bin, Version: version}
}

func (a *OfficialInstallArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Account, "The account it is installed for, and run as.")
	an.Describe(&a.Home, "That account's home, which the installers write under.")
	an.Describe(&a.Installer, "The vendor's install command, run by bash as the account with HOME set: for example `curl -fsSL https://claude.ai/install.sh | bash`.")
	an.Describe(&a.Bin, "The executable the installer leaves behind. Its presence is what installed means.")
}

func (s *OfficialInstallState) Annotate(an infer.Annotator) {
	an.Describe(&s.Version, "What the tool said its version was when last read. Reported, never enforced.")
}

func (r *OfficialInstall) Annotate(an infer.Annotator) {
	an.Describe(&r, "A self-updating tool, installed the vendor's way and then left alone.")
	an.SetToken("index", "OfficialInstall")
}

// asAccount is the command that runs something as the account, with a login-shaped environment.
func asAccount(account, home, command string) string {
	// cd first: runuser keeps the caller's working directory, usually a home the account cannot
	// enter, and Codex's installer then fails in find ("Failed to restore initial working directory")
	return "runuser -u " + shellQuote(account) + " -- env HOME=" + shellQuote(home) + " " +
		"PATH=" + shellQuote(home+"/.local/bin:/usr/local/bin:/usr/bin:/bin") + " bash -c " + shellQuote(`cd "$HOME" && `+command)
}

// readToolVersion is the version the tool reports, or ok false when it is not installed or does not
// run: a home carried over from another machine can hold an executable for the wrong architecture.
func readToolVersion(ctx context.Context, m *machine, account, bin string, home *string) (string, bool, error) {
	version := shellQuote(bin) + " --version 2>/dev/null"
	var command string
	if home == nil {
		// runuser supplies HOME from the account database for an ID-only import.
		command = "runuser -u " + shellQuote(account) + " -- bash -c " +
			shellQuote(`cd "$HOME" && PATH="$HOME/.local/bin:/usr/local/bin:/usr/bin:/bin" `+version)
	} else {
		command = asAccount(account, *home, version)
	}
	asked, err := m.ask(ctx, m.escalate("test -x "+shellQuote(bin)+" || exit 9; "+command))
	if err != nil || asked.code != 0 {
		return "", false, err
	}
	first := strings.Split(jscompat.Trim(asked.out), "\n")[0]
	if first == "" {
		first = "installed"
	}
	return first, true, nil
}

func ensureInstalled(ctx context.Context, m *machine, args OfficialInstallArgs) (OfficialInstallState, error) {
	version, ok, err := readToolVersion(ctx, m, args.Account, args.Bin, args.Home)
	if err != nil {
		return OfficialInstallState{}, err
	}
	if !ok {
		if _, err := m.must(ctx, m.escalate(asAccount(args.Account, deref(args.Home), deref(args.Installer)))); err != nil {
			return OfficialInstallState{}, err
		}
		if version, ok, err = readToolVersion(ctx, m, args.Account, args.Bin, args.Home); err != nil {
			return OfficialInstallState{}, err
		}
		if !ok {
			return OfficialInstallState{}, fmt.Errorf("the installer ran for %s but %s is still not there: %s", args.Account, args.Bin, deref(args.Installer))
		}
	}
	return installState(args, version), nil
}

// Create runs the installer when the tool is not there.
func (OfficialInstall) Create(ctx context.Context, req infer.CreateRequest[OfficialInstallArgs]) (infer.CreateResponse[OfficialInstallState], error) {
	id := req.Inputs.Account + ":" + req.Inputs.Bin
	if req.DryRun {
		return infer.CreateResponse[OfficialInstallState]{ID: id, Output: installState(req.Inputs, "")}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[OfficialInstallState]{}, err
	}
	state, err := ensureInstalled(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[OfficialInstallState]{}, err
	}
	return infer.CreateResponse[OfficialInstallState]{ID: id, Output: state}, nil
}

// Read asks the tool its version; gone from the machine, it is dropped from the state and the next
// deployment installs it again. An ID-only import supplies name@account@bin.
func (OfficialInstall) Read(ctx context.Context, req infer.ReadRequest[OfficialInstallArgs, OfficialInstallState]) (infer.ReadResponse[OfficialInstallArgs, OfficialInstallState], error) {
	id, inputs, state := req.ID, req.Inputs, req.State
	if state.Bin == "" && inputs.Bin != "" {
		state = installState(inputs, "")
	}
	if state.Bin == "" {
		parts := strings.SplitN(id, "@", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return infer.ReadResponse[OfficialInstallArgs, OfficialInstallState]{}, fmt.Errorf("official install import ID must be name@account@bin")
		}
		id, inputs.Account, inputs.Bin = parts[0], parts[1], parts[2]
		state = installState(inputs, "")
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[OfficialInstallArgs, OfficialInstallState]{}, err
	}
	version, ok, err := readToolVersion(ctx, m, state.Account, state.Bin, state.Home)
	if err != nil || !ok {
		return infer.ReadResponse[OfficialInstallArgs, OfficialInstallState]{}, err
	}
	state.Version = version
	return infer.ReadResponse[OfficialInstallArgs, OfficialInstallState]{ID: id, Inputs: inputs, State: state}, nil
}

// Update makes sure it is installed, under the new arguments.
func (OfficialInstall) Update(ctx context.Context, req infer.UpdateRequest[OfficialInstallArgs, OfficialInstallState]) (infer.UpdateResponse[OfficialInstallState], error) {
	// Import already verified the tool. This first update only records the declaration.
	if req.DryRun || req.State.Home == nil || req.State.Installer == nil {
		return infer.UpdateResponse[OfficialInstallState]{Output: installState(req.Inputs, req.State.Version)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[OfficialInstallState]{}, err
	}
	state, err := ensureInstalled(ctx, m, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[OfficialInstallState]{}, err
	}
	return infer.UpdateResponse[OfficialInstallState]{Output: state}, nil
}

// Diff: the version is deliberately not compared, updating is the tool's job. A different account
// or executable is a different install.
func (OfficialInstall) Diff(_ context.Context, req infer.DiffRequest[OfficialInstallArgs, OfficialInstallState]) (p.DiffResponse, error) {
	old, args := req.State, req.Inputs
	resp := p.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	replace := old.Account != args.Account || old.Bin != args.Bin
	for key, changed := range map[string]bool{
		"installer": old.Installer != nil && *old.Installer != deref(args.Installer), "bin": old.Bin != args.Bin,
		"account": old.Account != args.Account, "home": old.Home != nil && *old.Home != deref(args.Home),
	} {
		if !changed {
			continue
		}
		kind := p.Update
		if replace && (key == "account" || key == "bin") {
			kind = p.UpdateReplace
		}
		resp.DetailedDiff[key] = p.PropertyDiff{Kind: kind}
	}
	// Record the declaration once so subsequent edits compare against real inputs.
	resp.HasChanges = old.Home == nil || old.Installer == nil || len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete leaves it in place: an installed tool holds the account's own settings and sessions, and a
// deployment that stops declaring it is not a reason to take those away.
func (OfficialInstall) Delete(context.Context, infer.DeleteRequest[OfficialInstallState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
