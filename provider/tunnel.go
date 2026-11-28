package homelab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/tunnel"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// SshTunnel is a reverse ssh tunnel kept up by autossh under a systemd unit. Ported from
// src/resources/tunnel.ts.
//
// It is checked on three rungs: the unit file says what was declared, the unit is active, and the
// running process carries the declared forwards. Configurations that would loop a failing login
// until fail2ban bans the address are refused before anything is written.
type SshTunnel struct{}

// TunnelForward is one reverse forward: a port on the far end reaching a port here.
type TunnelForward struct {
	Remote int     `pulumi:"remote"`
	Local  int     `pulumi:"local"`
	Bind   *string `pulumi:"bind,optional"`
	What   *string `pulumi:"what,optional"`
}

// SshTunnelArgs are the inputs of an SshTunnel.
type SshTunnelArgs struct {
	To         string          `pulumi:"to"`
	Port       *int            `pulumi:"port,optional"`
	Identity   string          `pulumi:"identity"`
	RunAs      string          `pulumi:"runAs"`
	Forwards   []TunnelForward `pulumi:"forwards"`
	RestartSec *float64        `pulumi:"restartSec,optional"`
	Unit       *string         `pulumi:"unit,optional"`
	Directory  *string         `pulumi:"directory,optional"`
}

// SshTunnelState is what is kept in state.
type SshTunnelState struct {
	To         string          `pulumi:"to"`
	Port       int             `pulumi:"port"`
	Identity   string          `pulumi:"identity"`
	RunAs      string          `pulumi:"runAs"`
	Forwards   []TunnelForward `pulumi:"forwards"`
	RestartSec float64         `pulumi:"restartSec"`
	Unit       string          `pulumi:"unit"`
	Directory  string          `pulumi:"directory"`
	File       string          `pulumi:"file"`
	Active     bool            `pulumi:"active"`
	Running    []TunnelForward `pulumi:"running"`
	Restarts   int             `pulumi:"restarts"`
}

const (
	tunnelUnits        = "/etc/systemd/system"
	tunnelPort         = 22
	tunnelRestartSec   = 10.0
	tunnelRestartFloor = 5.0
	tunnelFileMarker   = "#pulumi-homelab#file"
	tunnelCmdMarker    = "#pulumi-homelab#cmdline"
)

func (f *TunnelForward) Annotate(a infer.Annotator) {
	a.Describe(&f.Remote, "The port published on the far end.")
	a.Describe(&f.Local, "The port on this machine it reaches.")
	a.Describe(&f.Bind, "A bind address for the far end to accept or ignore.")
	a.Describe(&f.What, "What the forward is for, written into the unit as a comment.")
}

func (s *SshTunnelArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.To, "Where the tunnel goes, as user@host.")
	a.Describe(&s.Port, "The far end's ssh port.")
	a.SetDefault(&s.Port, tunnelPort)
	a.Describe(&s.Identity, "The private key the tunnel offers, and the only one.")
	a.Describe(&s.RunAs, "The account the tunnel runs as, whose known_hosts must trust the far end.")
	a.Describe(&s.Forwards, "The reverse forwards, all carried on one connection.")
	a.Describe(&s.RestartSec, "Seconds between restarts. At least 5: a tight loop of failing logins gets the address banned.")
	a.SetDefault(&s.RestartSec, tunnelRestartSec)
	a.Describe(&s.Unit, "The unit name, without .service. Derived from the destination and port when not given.")
	a.Describe(&s.Directory, "Where the unit file goes.")
	a.SetDefault(&s.Directory, tunnelUnits)
}

func (s *SshTunnelState) Annotate(a infer.Annotator) {
	a.Describe(&s.File, "The unit file as the machine has it.")
	a.Describe(&s.Active, "Whether systemd says the unit is active.")
	a.Describe(&s.Running, "The forwards the running process carries, read from /proc.")
	a.Describe(&s.Restarts, "How many times systemd has restarted the unit.")
}

func (s *SshTunnel) Annotate(a infer.Annotator) {
	a.Describe(&s, "A reverse ssh tunnel kept up by autossh under systemd, checked against the running process.")
	a.SetToken("index", "SshTunnel")
}

func (f TunnelForward) bind() string { return deref(f.Bind) }

// restartRefusal says why a restart interval is too short, or "".
func restartRefusal(seconds float64) string {
	if seconds >= tunnelRestartFloor {
		return ""
	}
	s := jscompat.FormatNumber(seconds)
	return "RestartSec of " + s + "s is too short. ExitOnForwardFailure makes ssh exit when a port is " +
		"still held by the previous connection, systemd restarts it, and a tight loop is a login " +
		"attempt every " + s + " seconds until fail2ban bans this address — which takes out every " +
		"tunnel and the route needed to fix them. Use " + jscompat.FormatNumber(tunnelRestartSec) + "s, or at least " +
		jscompat.FormatNumber(tunnelRestartFloor) + "s."
}

// forwardFlag is the -R argument: [bind:]remote:localhost:local.
func forwardFlag(f TunnelForward) string {
	bind := ""
	if f.bind() != "" {
		bind = f.bind() + ":"
	}
	return fmt.Sprintf("%s%d:localhost:%d", bind, f.Remote, f.Local)
}

func inPortRange(port int) bool { return port > 0 && port < 65536 }

// forwardsRefusal says why a list of forwards cannot be one tunnel, or "".
func forwardsRefusal(forwards []TunnelForward) string {
	if len(forwards) == 0 {
		return "a tunnel with no forwards is a connection, not a tunnel"
	}
	for at, f := range forwards {
		if slices.IndexFunc(forwards, func(g TunnelForward) bool { return g.Remote == f.Remote }) != at {
			return fmt.Sprintf("two forwards both want port %d on the far end. The second would be refused, and "+
				"with ExitOnForwardFailure that means the whole tunnel exits — including the forwards that "+
				"were fine.", f.Remote)
		}
	}
	for _, f := range forwards {
		if !inPortRange(f.Remote) || !inPortRange(f.Local) {
			return fmt.Sprintf("%d:%d is not a pair of ports", f.Remote, f.Local)
		}
	}
	return ""
}

var unitName = regexp.MustCompile(`^[A-Za-z0-9_@.-]+$`)

// unitRefusal says why a unit name is not one, or "".
func unitRefusal(unit string) string {
	if unitName.MatchString(unit) && !strings.HasSuffix(unit, ".service") {
		return ""
	}
	if strings.HasSuffix(unit, ".service") {
		return "unit '" + unit + "' should not carry the .service suffix — it is added."
	}
	return "unit '" + unit + "' is not a systemd unit name: letters, digits, and _ @ . - only."
}

func farHost(to string) string {
	if i := strings.Index(to, "@"); i >= 0 {
		return to[i+1:]
	}
	return to
}

// unitNameFor derives autossh-<host>-<port>, with everything but letters, digits, _ and - made a
// dash, per UTF-16 code unit as the JavaScript replace did.
func unitNameFor(to string, port int) string {
	var b strings.Builder
	for _, r := range farHost(to) {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return fmt.Sprintf("autossh-%s-%d", b.String(), port)
}

// resolvedTunnel is a declaration settled: defaults filled in and refusals raised.
type resolvedTunnel struct {
	To, Identity, RunAs, Unit, Directory string
	Port                                 int
	RestartSec                           float64
	Forwards                             []TunnelForward
}

func resolveTunnel(args SshTunnelArgs) (resolvedTunnel, error) {
	port := tunnelPort
	if args.Port != nil {
		port = *args.Port
	}
	restartSec := tunnelRestartSec
	if args.RestartSec != nil {
		restartSec = *args.RestartSec
	}
	unit := unitNameFor(args.To, port)
	if args.Unit != nil {
		unit = *args.Unit
	}
	refusal := restartRefusal(restartSec)
	if refusal == "" {
		refusal = forwardsRefusal(args.Forwards)
	}
	if refusal == "" {
		refusal = unitRefusal(unit)
	}
	if refusal != "" {
		return resolvedTunnel{}, errors.New(refusal)
	}
	directory := tunnelUnits
	if args.Directory != nil {
		directory = *args.Directory
	}
	return resolvedTunnel{To: args.To, Port: port, Identity: args.Identity, RunAs: args.RunAs, Forwards: args.Forwards,
		RestartSec: restartSec, Unit: unit, Directory: directory}, nil
}

// execStart is the autossh command line: one connection carrying every forward.
func execStart(t resolvedTunnel) string {
	parts := []string{"/usr/bin/autossh", "-M", "0", "-N",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=30", "-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-i", t.Identity, "-p", fmt.Sprint(t.Port)}
	for _, f := range t.Forwards {
		parts = append(parts, "-R", forwardFlag(f))
	}
	return strings.Join(append(parts, t.To), " ")
}

// tunnelUnit is the unit file.
func tunnelUnit(t resolvedTunnel) string {
	lines := []string{"[Unit]", "Description=Reverse tunnel to " + t.To}
	for _, f := range t.Forwards {
		what := ""
		if deref(f.What) != "" {
			what = " (" + *f.What + ")"
		}
		lines = append(lines, fmt.Sprintf("# %d -> %d%s", f.Remote, f.Local, what))
	}
	lines = append(lines, "After=network-online.target", "Wants=network-online.target", "",
		"[Service]", "User="+t.RunAs, "Environment=AUTOSSH_GATETIME=0", "ExecStart="+execStart(t),
		"Restart=always", "RestartSec="+jscompat.FormatNumber(t.RestartSec), "",
		"[Install]", "WantedBy=multi-user.target", "")
	return strings.Join(lines, "\n")
}

// sameForwards compares two lists of forwards by what they do: order and comments do not count, a
// bind address does.
func sameForwards(a, b []TunnelForward) bool {
	shape := func(forwards []TunnelForward) string {
		flags := make([]string, len(forwards))
		for i, f := range forwards {
			flags[i] = forwardFlag(f)
		}
		slices.SortFunc(flags, jscompat.CompareUTF16)
		return strings.Join(flags, " ")
	}
	return shape(a) == shape(b)
}

// writeCommand stages the unit in a private directory, verifies it with systemd-analyze,
// installs it and reloads systemd, with the heredoc terminator alone on its line.
func writeCommand(unit, path, file string) string {
	staged := `"$dir"/` + shellQuote(unit+".service")
	return `dir=$(mktemp -d) && trap 'rm -rf "$dir"' EXIT && ` +
		heredocInto(staged, file,
			"systemd-analyze verify "+staged,
			"install -m 0644 -o root -g root "+staged+" "+shellQuote(path),
			"systemctl daemon-reload")
}

func readTunnelCommand(unit, directory string) string {
	path := shellQuote(unitPath(unit, directory))
	service := shellQuote(unit + ".service")
	return "test -f " + path + " || exit 9; " +
		"cat " + path + "; echo '" + tunnelFileMarker + "'; " +
		"systemctl show -p ActiveState -p NRestarts -p MainPID " + service + "; echo '" + tunnelCmdMarker + "'; " +
		"pid=$(systemctl show -p MainPID --value " + service + " 2>/dev/null); " +
		`[ -n "$pid" ] && [ "$pid" != 0 ] && tr '\0' '\n' < "/proc/$pid/cmdline" 2>/dev/null || true`
}

type tunnelRead struct {
	file     string
	active   bool
	restarts int
	running  []TunnelForward
}

func readTunnel(ctx context.Context, m *machine, unit, directory string) (*tunnelRead, error) {
	asked, err := m.ask(ctx, m.escalate(readTunnelCommand(unit, directory)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read the tunnel %s: %s", unit, jscompat.Trim(asked.err))
	}
	parts := strings.Split(asked.out, tunnelFileMarker+"\n")
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	parsed := tunnel.ParseTunnel(rest)
	running := make([]TunnelForward, len(parsed.Running))
	for i, f := range parsed.Running {
		running[i] = TunnelForward{Remote: f.Remote, Local: f.Local}
		if f.Bind != "" {
			bind := f.Bind
			running[i].Bind = &bind
		}
	}
	return &tunnelRead{file: parts[0], active: parsed.Active, restarts: parsed.Restarts, running: running}, nil
}

// preconditionRefusal says why the tunnel could not connect, before anything is installed.
func preconditionRefusal(identity, knownHost bool, t resolvedTunnel) string {
	if !identity {
		return t.RunAs + " cannot read " + t.Identity + " on this machine, so the tunnel would exit on " +
			"every start. Check the file exists and its mode and owner let " + t.RunAs + " read it."
	}
	if !knownHost {
		host := farHost(t.To)
		return t.RunAs + " does not trust " + host + "'s host key, so ssh would refuse under BatchMode and " +
			"the tunnel would restart for ever without ever connecting. Add it first: " +
			fmt.Sprintf("sudo -u %s ssh-keyscan -p %d %s >> ~%s/.ssh/known_hosts", t.RunAs, t.Port, host, t.RunAs)
	}
	return ""
}

func probeCommand(t resolvedTunnel) string {
	as := func(command string) string {
		return "sudo -n -u " + shellQuote(t.RunAs) + " sh -c " + shellQuote(command)
	}
	far := farHost(t.To)
	if t.Port != 22 {
		far = fmt.Sprintf("[%s]:%d", far, t.Port)
	}
	return as("test -r "+shellQuote(t.Identity)) + " && echo identity || true; " +
		as("ssh-keygen -F "+shellQuote(far)+" -q") + " && echo known || true"
}

func settleTunnel(ctx context.Context, m *machine, args SshTunnelArgs) (SshTunnelState, error) {
	wanted, err := resolveTunnel(args)
	if err != nil {
		return SshTunnelState{}, err
	}
	probed, err := m.ask(ctx, m.escalate(probeCommand(wanted)))
	if err != nil {
		return SshTunnelState{}, err
	}
	if refusal := preconditionRefusal(strings.Contains(probed.out, "identity"), strings.Contains(probed.out, "known"), wanted); refusal != "" {
		return SshTunnelState{}, errors.New(refusal)
	}
	file := tunnelUnit(wanted)
	path := unitPath(wanted.Unit, wanted.Directory)
	before, err := readTunnel(ctx, m, wanted.Unit, wanted.Directory)
	if err != nil {
		return SshTunnelState{}, err
	}
	service := shellQuote(wanted.Unit + ".service")
	rewrite := before == nil || before.file != file
	bounce := rewrite || !before.active || !sameForwards(before.running, wanted.Forwards)
	if rewrite {
		wrote, err := m.ask(ctx, m.escalate(writeCommand(wanted.Unit, path, file)))
		if err != nil {
			return SshTunnelState{}, err
		}
		if wrote.code != 0 {
			detail := wrote.err
			if detail == "" {
				detail = wrote.out
			}
			return SshTunnelState{}, fmt.Errorf("could not install %s on %s (exit %d): %s",
				path, m.describe(), wrote.code, jscompat.Trim(detail))
		}
	}
	if rewrite || bounce {
		command := "systemctl enable " + service
		if bounce {
			command += " && systemctl restart " + service
		}
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return SshTunnelState{}, err
		}
	}
	after, err := readTunnel(ctx, m, wanted.Unit, wanted.Directory)
	if err != nil {
		return SshTunnelState{}, err
	}
	if after == nil {
		return SshTunnelState{}, fmt.Errorf("wrote %s on %s but it is not there", path, m.describe())
	}
	if after.file != file {
		return SshTunnelState{}, fmt.Errorf("wrote %s on %s and it does not read back as written. "+
			"Something else is editing the same file, or the write itself was malformed.", path, m.describe())
	}
	if !sameForwards(after.running, wanted.Forwards) {
		carried := make([]string, len(after.running))
		for i, f := range after.running {
			carried[i] = forwardFlag(f)
		}
		running := strings.Join(carried, " ")
		if running == "" {
			running = "no forwards"
		}
		declared := make([]string, len(wanted.Forwards))
		for i, f := range wanted.Forwards {
			declared[i] = forwardFlag(f)
		}
		hint := "Check the journal for the unit."
		if after.restarts > 0 {
			hint = fmt.Sprintf("It has restarted %d times, which is what a forward the far end refuses looks like — "+
				"check whether something already holds those ports on %s.", after.restarts, wanted.To)
		}
		return SshTunnelState{}, fmt.Errorf("started %s on %s but the running process carries %s rather than %s. %s",
			wanted.Unit, m.describe(), running, strings.Join(declared, " "), hint)
	}
	return tunnelState(wanted, after), nil
}

func tunnelState(w resolvedTunnel, r *tunnelRead) SshTunnelState {
	s := SshTunnelState{To: w.To, Port: w.Port, Identity: w.Identity, RunAs: w.RunAs, Forwards: w.Forwards,
		RestartSec: w.RestartSec, Unit: w.Unit, Directory: w.Directory, Running: []TunnelForward{}}
	if r != nil {
		s.File, s.Active, s.Running, s.Restarts = r.file, r.active, r.running, r.restarts
	}
	return s
}

// Check refuses a restart loop and forwards that cannot be one tunnel, at preview.
func (SshTunnel) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SshTunnelArgs], error) {
	args, failures, err := infer.DefaultCheck[SshTunnelArgs](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[SshTunnelArgs]{Inputs: args, Failures: failures}, err
	}
	restartSec := tunnelRestartSec
	if args.RestartSec != nil {
		restartSec = *args.RestartSec
	}
	bad := restartRefusal(restartSec)
	if bad == "" {
		bad = forwardsRefusal(args.Forwards)
	}
	if bad != "" {
		failures = append(failures, p.CheckFailure{Property: "forwards", Reason: bad})
	}
	return infer.CheckResponse[SshTunnelArgs]{Inputs: args, Failures: failures}, nil
}

// Create installs, enables and starts the unit, and checks the process carries the forwards.
func (SshTunnel) Create(ctx context.Context, req infer.CreateRequest[SshTunnelArgs]) (infer.CreateResponse[SshTunnelState], error) {
	if req.DryRun {
		wanted, err := resolveTunnel(req.Inputs)
		if err != nil {
			return infer.CreateResponse[SshTunnelState]{}, err
		}
		return infer.CreateResponse[SshTunnelState]{ID: wanted.Unit, Output: tunnelState(wanted, nil)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SshTunnelState]{}, err
	}
	state, err := settleTunnel(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[SshTunnelState]{}, err
	}
	return infer.CreateResponse[SshTunnelState]{ID: state.Unit, Output: state}, nil
}

// Read reports the unit file, whether it is active and what the process carries; a unit file gone
// is forgotten. On import the declaration is taken from the inputs when given, and otherwise the
// destination from the unit's description.
func (SshTunnel) Read(ctx context.Context, req infer.ReadRequest[SshTunnelArgs, SshTunnelState]) (infer.ReadResponse[SshTunnelArgs, SshTunnelState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SshTunnelArgs, SshTunnelState]{}, err
	}
	directory := req.State.Directory
	if directory == "" {
		directory = deref(req.Inputs.Directory)
		if directory == "" {
			directory = tunnelUnits
		}
	}
	found, err := readTunnel(ctx, m, req.ID, directory)
	if err != nil {
		return infer.ReadResponse[SshTunnelArgs, SshTunnelState]{}, err
	}
	if found == nil {
		return infer.ReadResponse[SshTunnelArgs, SshTunnelState]{}, nil
	}
	state := req.State
	inputs := req.Inputs
	if state.Unit == "" {
		// an import
		state = SshTunnelState{To: inputs.To, Port: tunnelPort, Identity: inputs.Identity, RunAs: inputs.RunAs,
			Forwards: inputs.Forwards, RestartSec: tunnelRestartSec}
		if inputs.Port != nil {
			state.Port = *inputs.Port
		}
		if inputs.RestartSec != nil {
			state.RestartSec = *inputs.RestartSec
		}
		if state.To == "" {
			for _, line := range strings.Split(found.file, "\n") {
				if to, ok := strings.CutPrefix(line, "Description=Reverse tunnel to "); ok {
					state.To = to
					break
				}
			}
		}
		if state.Forwards == nil {
			state.Forwards = found.running
		}
		if inputs.To == "" {
			unit := req.ID
			inputs = SshTunnelArgs{To: state.To, Port: &state.Port, Identity: state.Identity, RunAs: state.RunAs,
				Forwards: state.Forwards, RestartSec: &state.RestartSec, Unit: &unit, Directory: &directory}
		}
	}
	state.Unit, state.Directory = req.ID, directory
	state.File, state.Active, state.Running, state.Restarts = found.file, found.active, found.running, found.restarts
	return infer.ReadResponse[SshTunnelArgs, SshTunnelState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the tunnel under its unit name.
func (SshTunnel) Update(ctx context.Context, req infer.UpdateRequest[SshTunnelArgs, SshTunnelState]) (infer.UpdateResponse[SshTunnelState], error) {
	args := req.Inputs
	unit := req.ID
	args.Unit = &unit
	if req.DryRun {
		wanted, err := resolveTunnel(args)
		if err != nil {
			return infer.UpdateResponse[SshTunnelState]{}, err
		}
		return infer.UpdateResponse[SshTunnelState]{Output: tunnelState(wanted, nil)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SshTunnelState]{}, err
	}
	state, err := settleTunnel(ctx, m, args)
	if err != nil {
		return infer.UpdateResponse[SshTunnelState]{}, err
	}
	return infer.UpdateResponse[SshTunnelState]{Output: state}, nil
}

// Diff: the unit file differing, the unit inactive, or the process carrying other forwards is an
// update; a new destination or unit name replaces, deleting the old unit first.
func (SshTunnel) Diff(_ context.Context, req infer.DiffRequest[SshTunnelArgs, SshTunnelState]) (p.DiffResponse, error) {
	old := req.State
	args := req.Inputs
	unit := old.Unit
	args.Unit = &unit
	wanted, err := resolveTunnel(args)
	if err != nil {
		return p.DiffResponse{}, err
	}
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if old.File != tunnelUnit(wanted) || !old.Active || !sameForwards(old.Running, wanted.Forwards) {
		resp.DetailedDiff["forwards"] = p.PropertyDiff{Kind: p.Update}
	}
	// wanted is resolved under the old unit name, as the TypeScript did, so of to and unit only a
	// new destination can differ here, and a new unit input is settled under the old id
	if old.To != wanted.To || old.Unit != wanted.Unit {
		resp.DetailedDiff["to"] = p.PropertyDiff{Kind: p.UpdateReplace}
		resp.DetailedDiff["unit"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

func tunnelDeleteCommand(unit, path string) string {
	service := shellQuote(unit + ".service")
	file := shellQuote(path)
	disable := "systemctl disable --now " + service
	// If the file is gone, systemd may still have a running unit. Only skip
	// disabling it when systemd confirms the unit is missing and inactive.
	return "if [ -e " + file + " ] || [ -L " + file + " ]; then " + disable + "; else\n" +
		"  state=$(systemctl show --property=LoadState --property=ActiveState " + service + ") || exit $?\n" +
		"  case \"$state\" in\n" +
		"    'LoadState=not-found\nActiveState=inactive'|'ActiveState=inactive\nLoadState=not-found') ;;\n" +
		"    *) " + disable + " ;;\n" +
		"  esac\n" +
		"fi && rm -f " + file + " && systemctl daemon-reload"
}

func (SshTunnel) Delete(ctx context.Context, req infer.DeleteRequest[SshTunnelState]) (infer.DeleteResponse, error) {
	directory := req.State.Directory
	if directory == "" {
		directory = tunnelUnits
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(tunnelDeleteCommand(req.ID, unitPath(req.ID, directory))))
	return infer.DeleteResponse{}, err
}
