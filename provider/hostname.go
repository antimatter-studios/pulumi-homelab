package homelab

import (
	"context"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/hosts"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// Hostname is the machine's name, set with hostnamectl and written on the 127.0.1.1 line of
// /etc/hosts so the two agree. Ported from src/resources/hostname.ts. Delete does nothing: a
// machine always has a name, and the stack ceasing to describe it is no reason to change it.
type Hostname struct{}

// HostnameArgs are the inputs of a Hostname.
type HostnameArgs struct {
	Name         string  `pulumi:"name"`
	Domain       *string `pulumi:"domain,optional"`
	RestartAvahi *bool   `pulumi:"restartAvahi,optional"`
	Hosts        *string `pulumi:"hosts,optional"`
}

// HostnameEffective is what the machine reports.
type HostnameEffective struct {
	Static    string `pulumi:"static"`
	Transient string `pulumi:"transient"`
	HostsLine string `pulumi:"hostsLine"`
	NamesHost bool   `pulumi:"namesHost"`
}

// HostnameState is what is kept in state.
type HostnameState struct {
	Name         string            `pulumi:"name"`
	Domain       string            `pulumi:"domain"`
	RestartAvahi bool              `pulumi:"restartAvahi"`
	Hosts        string            `pulumi:"hosts"`
	Effective    HostnameEffective `pulumi:"effective"`
}

func (a *HostnameArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The short name.")
	an.Describe(&a.Domain, "A domain, written as <name>.<domain> before the short name on the 127.0.1.1 line.")
	an.Describe(&a.RestartAvahi, "Whether avahi-daemon is restarted after a rename, so mDNS announces the new name. On by default.")
	an.Describe(&a.Hosts, "The hosts file, /etc/hosts by default.")
}

func (r *Hostname) Annotate(a infer.Annotator) {
	a.Describe(&r, "The machine's name, with /etc/hosts agreeing with it.")
	a.SetToken("index", "Hostname")
}

const (
	hostsFile     = "/etc/hosts"
	hostsLoopback = "127.0.1.1"
	hostnameMark  = "#pulumi-homelab#"
)

type hostnameRead struct {
	hosts.HostnameOutput
	hostsLine string
}

func hostnameReadCommand(hostsPath string) string {
	return "hostnamectl --static 2>/dev/null || cat /etc/hostname 2>/dev/null || true; " +
		"echo " + shellQuote(hostnameMark+"now") + "; hostname 2>/dev/null || true; " +
		"echo " + shellQuote(hostnameMark+"hosts") + "; cat " + shellQuote(hostsPath) + " 2>/dev/null || true"
}

func readHostname(ctx context.Context, m *machine, hostsPath string) (hostnameRead, error) {
	asked, err := m.ask(ctx, m.escalate(hostnameReadCommand(hostsPath)))
	if err != nil {
		return hostnameRead{}, err
	}
	if asked.code != 0 {
		return hostnameRead{}, fmt.Errorf("could not read the hostname on %s: %s", m.describe(), jscompat.Trim(asked.err))
	}
	found := hosts.ParseHostnameOutput(asked.out)
	line, _ := hosts.HostsName(found.HostsFile)
	return hostnameRead{HostnameOutput: found, hostsLine: line}, nil
}

// hostnameSettleCommand renames and rewrites the hosts file as needed, or "" for nothing to do.
func hostnameSettleCommand(name, hostsPath, current, updated string, renaming bool) string {
	var steps []string
	if renaming {
		steps = append(steps, "hostnamectl set-hostname "+shellQuote(name))
	}
	if updated != current {
		steps = append(steps, heredoc(hostsPath, updated))
	}
	return strings.Join(steps, " && ")
}

func settleHostname(ctx context.Context, m *machine, a HostnameArgs) (HostnameState, error) {
	hostsPath, domain := orDefault(a.Hosts, hostsFile), orDefault(a.Domain, "")
	restartAvahi := true
	if a.RestartAvahi != nil {
		restartAvahi = *a.RestartAvahi
	}
	before, err := readHostname(ctx, m, hostsPath)
	if err != nil {
		return HostnameState{}, err
	}
	renaming := before.Static != a.Name
	current, err := m.must(ctx, m.escalate("cat "+shellQuote(hostsPath)+" 2>/dev/null || true"))
	if err != nil {
		return HostnameState{}, err
	}
	updated := hosts.SetHostsName(current, a.Name, domain)
	if command := hostnameSettleCommand(a.Name, hostsPath, current, updated, renaming); command != "" {
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return HostnameState{}, err
		}
		if renaming && restartAvahi {
			if _, err := m.must(ctx, m.escalate("systemctl try-restart avahi-daemon 2>/dev/null || true")); err != nil {
				return HostnameState{}, err
			}
		}
	}
	found, err := readHostname(ctx, m, hostsPath)
	if err != nil {
		return HostnameState{}, err
	}
	effective := HostnameEffective{Static: found.Static, Transient: found.Transient, HostsLine: found.hostsLine, NamesHost: hosts.HostsNamesHost(found.HostsFile, a.Name)}
	if effective.Static != a.Name {
		reports := effective.Static
		if reports == "" {
			reports = "nothing"
		}
		return HostnameState{}, fmt.Errorf("set the hostname to %s on %s but it reports %s", a.Name, m.describe(), reports)
	}
	if !effective.NamesHost {
		return HostnameState{}, fmt.Errorf("set the hostname to %s on %s but %s does not name it on the "+
			"%s line — the two disagreeing is what makes sudo slow and daemons bind wrong", a.Name, m.describe(), hostsPath, hostsLoopback)
	}
	return HostnameState{Name: a.Name, Domain: domain, RestartAvahi: restartAvahi, Hosts: hostsPath, Effective: effective}, nil
}

// domainOf is the domain the 127.0.1.1 line gives name: the rest of a `<name>.<domain>` entry.
func domainOf(hostsText, name string) string {
	for _, each := range hosts.HostsNames(hostsText) {
		if rest, ok := strings.CutPrefix(each, name+"."); ok && rest != "" {
			return rest
		}
	}
	return ""
}

// Create sets the name; the id is the hosts file.
func (Hostname) Create(ctx context.Context, req infer.CreateRequest[HostnameArgs]) (infer.CreateResponse[HostnameState], error) {
	id := orDefault(req.Inputs.Hosts, hostsFile)
	if req.DryRun {
		return infer.CreateResponse[HostnameState]{ID: id, Output: HostnameState{
			Name: req.Inputs.Name, Domain: orDefault(req.Inputs.Domain, ""), RestartAvahi: req.Inputs.RestartAvahi == nil || *req.Inputs.RestartAvahi, Hosts: id,
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[HostnameState]{}, err
	}
	state, err := settleHostname(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[HostnameState]{}, err
	}
	return infer.CreateResponse[HostnameState]{ID: id, Output: state}, nil
}

// Read reports what the machine is called; there is always a name, so it is never forgotten.
func (Hostname) Read(ctx context.Context, req infer.ReadRequest[HostnameArgs, HostnameState]) (infer.ReadResponse[HostnameArgs, HostnameState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[HostnameArgs, HostnameState]{}, err
	}
	found, err := readHostname(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[HostnameArgs, HostnameState]{}, err
	}
	state := req.State
	imported := state.Name == ""
	if imported {
		state.Name, state.RestartAvahi = found.Static, true
		state.Domain = domainOf(found.HostsFile, found.Static)
	}
	state.Hosts = req.ID
	state.Effective = HostnameEffective{Static: found.Static, Transient: found.Transient, HostsLine: found.hostsLine, NamesHost: hosts.HostsNamesHost(found.HostsFile, state.Name)}
	inputs := req.Inputs
	if imported {
		inputs = HostnameArgs{Name: state.Name, Domain: &state.Domain, RestartAvahi: &state.RestartAvahi, Hosts: &state.Hosts}
	}
	return infer.ReadResponse[HostnameArgs, HostnameState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update sets the name again, in the hosts file named by the id.
func (Hostname) Update(ctx context.Context, req infer.UpdateRequest[HostnameArgs, HostnameState]) (infer.UpdateResponse[HostnameState], error) {
	a := req.Inputs
	id := req.ID
	a.Hosts = &id
	if req.DryRun {
		s := req.State
		s.Name, s.Domain = a.Name, orDefault(a.Domain, "")
		return infer.UpdateResponse[HostnameState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[HostnameState]{}, err
	}
	state, err := settleHostname(ctx, m, a)
	return infer.UpdateResponse[HostnameState]{Output: state}, err
}

// Diff: the machine's static name (case-insensitively), the hosts line naming it, and the declared
// name and domain. Never a replacement.
func (Hostname) Diff(_ context.Context, req infer.DiffRequest[HostnameArgs, HostnameState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	changed := jscompat.ToLower(old.Effective.Static) != jscompat.ToLower(a.Name) || !old.Effective.NamesHost ||
		old.Name != a.Name || old.Domain != orDefault(a.Domain, "")
	return diffOf(changed, false), nil
}

// Delete does nothing: the machine keeps its name.
func (Hostname) Delete(context.Context, infer.DeleteRequest[HostnameState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
