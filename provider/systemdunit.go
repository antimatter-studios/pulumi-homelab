package homelab

import (
	"context"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
	"github.com/antimatter-studios/pulumi-homelab/provider/systemd"
)

// SystemdUnit is a service unit file, enabled and started as declared. Ported from
// src/resources/systemd.ts. Only what differs is done: a changed file is rewritten and restarts
// the service, an enablement change alone does not.
type SystemdUnit struct{}

// SystemdUnitArgs are the inputs of a SystemdUnit.
type SystemdUnitArgs struct {
	Name      string  `pulumi:"name"`
	Unit      string  `pulumi:"unit"`
	Enabled   *bool   `pulumi:"enabled,optional"`
	Started   *bool   `pulumi:"started,optional"`
	Mode      *string `pulumi:"mode,optional"`
	Directory *string `pulumi:"directory,optional"`
}

// SystemdUnitState is what is kept in state.
type SystemdUnitState struct {
	Name          string `pulumi:"name"`
	Unit          string `pulumi:"unit"`
	Enabled       bool   `pulumi:"enabled"`
	Started       bool   `pulumi:"started"`
	Mode          string `pulumi:"mode"`
	UnitFileState string `pulumi:"unitFileState"`
	Directory     string `pulumi:"directory"`
}

func (a *SystemdUnitArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The unit's name, without .service.")
	an.Describe(&a.Unit, "The unit file's content.")
	an.Describe(&a.Enabled, "Whether it starts at boot. On by default.")
	an.Describe(&a.Started, "Whether it is running now. On by default.")
	an.Describe(&a.Mode, "The unit file's mode, 0644 by default.")
	an.Describe(&a.Directory, "Where the unit file goes, /etc/systemd/system by default.")
}

func (s *SystemdUnitState) Annotate(an infer.Annotator) {
	an.Describe(&s.UnitFileState, "What systemctl show says the unit file state is.")
}

func (r *SystemdUnit) Annotate(a infer.Annotator) {
	a.Describe(&r, "A service the machine should run, checked against systemd rather than remembered.")
	a.SetToken("index", "SystemdUnit")
}

const systemdUnits = "/etc/systemd/system"

func unitPath(name, directory string) string { return directory + "/" + name + ".service" }

func unitReadCommand(name, directory string) string {
	file := unitPath(name, directory)
	return "test -f " + shellQuote(file) + " || exit 9; " +
		"systemctl show " + shellQuote(name) + " --property=UnitFileState,ActiveState | tr '\\n' ' ' && " +
		"stat -c '%a' " + shellQuote(file) + " && cat " + shellQuote(file)
}

// parseUnitRead reads the answer to unitReadCommand.
func parseUnitRead(out, directory string) SystemdUnitState {
	head, unit := firstLineSplit(out)
	first := jscompat.Split(jscompat.Trim(head))
	var assignments []string
	for _, w := range first {
		if strings.Contains(w, "=") {
			assignments = append(assignments, w)
		}
	}
	shown := systemd.ParseShow(strings.Join(assignments, "\n"))
	last := ""
	if len(first) > 0 {
		last = first[len(first)-1]
	}
	fileState, activeState := shown["UnitFileState"], shown["ActiveState"]
	return SystemdUnitState{
		Directory: directory, UnitFileState: fileState, Mode: mode.NormaliseMode(last),
		Enabled: strings.HasPrefix(fileState, "enabled"), Started: activeState == "active" || activeState == "activating",
		Unit: unit,
	}
}

func readUnit(ctx context.Context, m *machine, name, directory string) (*SystemdUnitState, error) {
	asked, err := m.ask(ctx, m.escalate(unitReadCommand(name, directory)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read unit %s: %s", name, jscompat.Trim(asked.err))
	}
	read := parseUnitRead(asked.out, directory)
	return &read, nil
}

type unitActs struct{ rewrite, relabel, bounce bool }

// actsNeeded is what has to happen to make current into wanted, and nothing else.
func actsNeeded(current *SystemdUnitState, wanted SystemdUnitState) unitActs {
	rewrite := current == nil || current.Unit != wanted.Unit || current.Mode != wanted.Mode
	relabel := current == nil || current.Enabled != wanted.Enabled
	bounce := rewrite || current == nil || current.Started != wanted.Started
	return unitActs{rewrite, relabel, bounce}
}

// unitApplyCommand is the one command for the acts needed, or "" when there are none.
func unitApplyCommand(s SystemdUnitState, current *SystemdUnitState) string {
	acts := actsNeeded(current, s)
	if !acts.rewrite && !acts.relabel && !acts.bounce {
		return ""
	}
	file, unit := unitPath(s.Name, s.Directory), shellQuote(s.Name)
	var steps []string
	if acts.rewrite {
		steps = append(steps, "chmod "+s.Mode+" "+shellQuote(file), "systemctl daemon-reload")
	}
	if acts.relabel {
		verb := "disable"
		if s.Enabled {
			verb = "enable"
		}
		steps = append(steps, "systemctl "+verb+" "+unit)
	}
	if acts.bounce {
		verb := "stop"
		if s.Started {
			verb = "restart"
		}
		steps = append(steps, "systemctl "+verb+" "+unit)
	}
	if acts.rewrite {
		return heredoc(file, s.Unit, steps...)
	}
	return strings.Join(steps, " && ")
}

// staticRefusal: a static unit has no [Install] section and cannot be enabled.
func staticRefusal(name string, wantEnabled bool, unitFileState string) error {
	if wantEnabled && unitFileState == "static" {
		return fmt.Errorf("%s is a static unit: it has no [Install] section, so it cannot be enabled. "+
			"Describe the unit that pulls it in, or set enabled: false. If you only need the file managed, "+
			"a ManagedFile at %s with reloadSystemd: true does that and "+
			"still tells systemd about the change.", name, unitPath(name, systemdUnits))
	}
	return nil
}

func unitWanted(a SystemdUnitArgs) SystemdUnitState {
	s := SystemdUnitState{Name: a.Name, Unit: a.Unit, Enabled: true, Started: true, Mode: orDefault(a.Mode, "0644"), Directory: orDefault(a.Directory, systemdUnits)}
	if a.Enabled != nil {
		s.Enabled = *a.Enabled
	}
	if a.Started != nil {
		s.Started = *a.Started
	}
	return s
}

func settleUnit(ctx context.Context, m *machine, wanted SystemdUnitState) (SystemdUnitState, error) {
	current, err := readUnit(ctx, m, wanted.Name, wanted.Directory)
	if err != nil {
		return wanted, err
	}
	if command := unitApplyCommand(wanted, current); command != "" {
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return wanted, err
		}
	}
	actual, err := readUnit(ctx, m, wanted.Name, wanted.Directory)
	if err != nil {
		return wanted, err
	}
	fileState := ""
	if actual != nil {
		fileState = actual.UnitFileState
	}
	if err := staticRefusal(wanted.Name, wanted.Enabled, fileState); err != nil {
		return wanted, err
	}
	if actual == nil {
		return wanted, nil
	}
	out := *actual
	out.Name = wanted.Name
	return out, nil
}

// Create writes, enables and starts as declared; the id is the name.
func (SystemdUnit) Create(ctx context.Context, req infer.CreateRequest[SystemdUnitArgs]) (infer.CreateResponse[SystemdUnitState], error) {
	wanted := unitWanted(req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[SystemdUnitState]{ID: wanted.Name, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SystemdUnitState]{}, err
	}
	state, err := settleUnit(ctx, m, wanted)
	if err != nil {
		return infer.CreateResponse[SystemdUnitState]{}, err
	}
	return infer.CreateResponse[SystemdUnitState]{ID: wanted.Name, Output: state}, nil
}

// Read reports the unit as systemd has it; its file gone, it is forgotten.
func (SystemdUnit) Read(ctx context.Context, req infer.ReadRequest[SystemdUnitArgs, SystemdUnitState]) (infer.ReadResponse[SystemdUnitArgs, SystemdUnitState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SystemdUnitArgs, SystemdUnitState]{}, err
	}
	imported := req.State.Name == ""
	directory := req.State.Directory
	if imported {
		directory = systemdUnits
	}
	actual, err := readUnit(ctx, m, req.ID, directory)
	if err != nil || actual == nil {
		return infer.ReadResponse[SystemdUnitArgs, SystemdUnitState]{}, err
	}
	state := *actual
	state.Name = req.ID
	inputs := req.Inputs
	if imported {
		inputs = SystemdUnitArgs{Name: req.ID, Unit: state.Unit, Enabled: &state.Enabled, Started: &state.Started, Mode: &state.Mode, Directory: &state.Directory}
	}
	return infer.ReadResponse[SystemdUnitArgs, SystemdUnitState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the unit under its id.
func (SystemdUnit) Update(ctx context.Context, req infer.UpdateRequest[SystemdUnitArgs, SystemdUnitState]) (infer.UpdateResponse[SystemdUnitState], error) {
	wanted := unitWanted(req.Inputs)
	wanted.Name = req.ID
	if req.DryRun {
		wanted.UnitFileState = req.State.UnitFileState
		return infer.UpdateResponse[SystemdUnitState]{Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SystemdUnitState]{}, err
	}
	state, err := settleUnit(ctx, m, wanted)
	return infer.UpdateResponse[SystemdUnitState]{Output: state}, err
}

// Diff: a rename replaces (delete before replace); content, enablement, running and mode update.
func (SystemdUnit) Diff(_ context.Context, req infer.DiffRequest[SystemdUnitArgs, SystemdUnitState]) (p.DiffResponse, error) {
	old, wanted := req.State, unitWanted(req.Inputs)
	changed := old.Unit != wanted.Unit || old.Enabled != wanted.Enabled || old.Started != wanted.Started || old.Mode != wanted.Mode
	var replaces []string
	if old.Name != wanted.Name {
		replaces = []string{"name"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete stops and disables the unit and removes its file. Whatever the service kept on disk is
// left alone.
func (SystemdUnit) Delete(ctx context.Context, req infer.DeleteRequest[SystemdUnitState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	// the directory in state as written, even an explicitly empty one: substituting the default
	// would remove a different unit of the same name (review of #98)
	directory := req.State.Directory
	_, err = m.must(ctx, m.escalate("systemctl disable --now "+shellQuote(req.ID)+" || true; "+
		"rm -f "+shellQuote(unitPath(req.ID, directory))+" && systemctl daemon-reload"))
	return infer.DeleteResponse{}, err
}
