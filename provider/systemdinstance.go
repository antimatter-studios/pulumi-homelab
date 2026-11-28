package homelab

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/systemd"
)

// SystemdInstance is one instance of a template unit, enabled and started as declared. Ported
// from src/resources/instance.ts. The template is somebody else's; delete only disables and stops
// the instance.
type SystemdInstance struct{}

// SystemdInstanceArgs are the inputs of a SystemdInstance.
type SystemdInstanceArgs struct {
	Template string  `pulumi:"template"`
	Instance string  `pulumi:"instance"`
	Enabled  *bool   `pulumi:"enabled,optional"`
	Started  *bool   `pulumi:"started,optional"`
	Suffix   *string `pulumi:"suffix,optional"`
}

// SystemdInstanceState is what is kept in state.
type SystemdInstanceState struct {
	Template string `pulumi:"template"`
	Instance string `pulumi:"instance"`
	Enabled  bool   `pulumi:"enabled"`
	Started  bool   `pulumi:"started"`
	Suffix   string `pulumi:"suffix"`
	Unit     string `pulumi:"unit"`
}

func (a *SystemdInstanceArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Template, "The template's name, without @ or suffix.")
	an.Describe(&a.Instance, "The instance, escaped as systemd-escape does.")
	an.Describe(&a.Enabled, "Whether it starts at boot. On by default.")
	an.Describe(&a.Started, "Whether it is running now. On by default.")
	an.Describe(&a.Suffix, "The unit type, service by default.")
}

func (s *SystemdInstanceState) Annotate(an infer.Annotator) {
	an.Describe(&s.Unit, "The unit's full name, as systemd knows it.")
}

func (r *SystemdInstance) Annotate(a infer.Annotator) {
	a.Describe(&r, "An instance of a template unit the machine should run.")
	a.SetToken("index", "SystemdInstance")
}

// instanceUnit is the name systemd knows the instance by.
func instanceUnit(template, instance, suffix string) string {
	return template + "@" + systemd.EscapeInstance(instance) + "." + suffix
}

// parseInstanceUnit splits `template@escaped.suffix` and undoes EscapeInstance, and accepts the
// name only when escaping the result gives it back exactly.
func parseInstanceUnit(unit string) (template, instance, suffix string, ok bool) {
	template, rest, found := strings.Cut(unit, "@")
	dot := strings.LastIndex(rest, ".")
	if !found || template == "" || dot < 0 || dot == len(rest)-1 {
		return "", "", "", false
	}
	escaped, suffix := rest[:dot], rest[dot+1:]
	var b []byte
	for i := 0; i < len(escaped); i++ {
		switch {
		case escaped[i] == '-':
			b = append(b, '/')
		case escaped[i] == '\\' && i+3 < len(escaped) && escaped[i+1] == 'x':
			n, err := strconv.ParseUint(escaped[i+2:i+4], 16, 8)
			if err != nil {
				return "", "", "", false
			}
			b = append(b, byte(n))
			i += 3
		default:
			b = append(b, escaped[i])
		}
	}
	instance = string(b)
	if instanceUnit(template, instance, suffix) != unit {
		return "", "", "", false
	}
	return template, instance, suffix, true
}

func instanceReadCommand(template, unit, suffix string) string {
	return "systemctl cat " + shellQuote(template+"@."+suffix) + " >/dev/null 2>&1 || exit 9; " +
		"systemctl is-enabled " + shellQuote(unit) + " 2>/dev/null || true; echo '#pulumi-homelab#'; " +
		"systemctl is-active " + shellQuote(unit) + " 2>/dev/null || true"
}

type instanceRead struct{ enabled, started bool }

func parseInstanceRead(out string) instanceRead {
	enabled, active := splitOnce(out, homelabMarker)
	active = jscompat.Trim(active)
	return instanceRead{enabled: strings.HasPrefix(jscompat.Trim(enabled), "enabled"), started: active == "active" || active == "activating"}
}

func readInstance(ctx context.Context, m *machine, template, unit, suffix string) (*instanceRead, error) {
	asked, err := m.ask(ctx, m.escalate(instanceReadCommand(template, unit, suffix)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", unit, jscompat.Trim(asked.err))
	}
	read := parseInstanceRead(asked.out)
	return &read, nil
}

func instanceWanted(a SystemdInstanceArgs) SystemdInstanceState {
	s := SystemdInstanceState{Template: a.Template, Instance: a.Instance, Enabled: true, Started: true, Suffix: orDefault(a.Suffix, "service")}
	if a.Enabled != nil {
		s.Enabled = *a.Enabled
	}
	if a.Started != nil {
		s.Started = *a.Started
	}
	s.Unit = instanceUnit(s.Template, s.Instance, s.Suffix)
	return s
}

func settleInstance(ctx context.Context, m *machine, wanted SystemdInstanceState) (SystemdInstanceState, error) {
	unit := wanted.Unit
	current, err := readInstance(ctx, m, wanted.Template, unit, wanted.Suffix)
	if err != nil {
		return wanted, err
	}
	if current == nil {
		return wanted, fmt.Errorf("there is no template %s@.%s on %s, so %s cannot exist. "+
			"Declare the file that defines it — a ManagedFile with reloadSystemd, or a SystemdUnit — and depend on it.",
			wanted.Template, wanted.Suffix, m.describe(), unit)
	}
	var steps []string
	if current.enabled != wanted.Enabled {
		verb := "disable"
		if wanted.Enabled {
			verb = "enable"
		}
		steps = append(steps, "systemctl "+verb+" "+shellQuote(unit))
	}
	if current.started != wanted.Started {
		verb := "stop"
		if wanted.Started {
			verb = "start"
		}
		steps = append(steps, "systemctl "+verb+" "+shellQuote(unit))
	}
	if len(steps) > 0 {
		if _, err := m.must(ctx, m.escalate(strings.Join(steps, " && "))); err != nil {
			return wanted, err
		}
	}
	actual, err := readInstance(ctx, m, wanted.Template, unit, wanted.Suffix)
	if err != nil {
		return wanted, err
	}
	if actual == nil {
		return wanted, fmt.Errorf("%s vanished while it was being configured", unit)
	}
	wanted.Enabled, wanted.Started = actual.enabled, actual.started
	return wanted, nil
}

// Create enables and starts the instance as declared; the id is the unit.
func (SystemdInstance) Create(ctx context.Context, req infer.CreateRequest[SystemdInstanceArgs]) (infer.CreateResponse[SystemdInstanceState], error) {
	wanted := instanceWanted(req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[SystemdInstanceState]{ID: wanted.Unit, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SystemdInstanceState]{}, err
	}
	state, err := settleInstance(ctx, m, wanted)
	if err != nil {
		return infer.CreateResponse[SystemdInstanceState]{}, err
	}
	return infer.CreateResponse[SystemdInstanceState]{ID: state.Unit, Output: state}, nil
}

// Read reports the instance; neither enabled nor running, or its template gone, it is forgotten.
func (SystemdInstance) Read(ctx context.Context, req infer.ReadRequest[SystemdInstanceArgs, SystemdInstanceState]) (infer.ReadResponse[SystemdInstanceArgs, SystemdInstanceState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SystemdInstanceArgs, SystemdInstanceState]{}, err
	}
	state := req.State
	imported := state.Template == ""
	if imported {
		// nothing remembered: template, instance and suffix all come out of the unit name, so the
		// imported inputs name the same unit and a .timer or .mount instance probes its own template
		template, instance, suffix, ok := parseInstanceUnit(req.ID)
		if !ok {
			template, _ = splitOnce(req.ID, "@")
			suffix = "service"
		}
		state.Template, state.Instance, state.Suffix = template, instance, suffix
	}
	actual, err := readInstance(ctx, m, state.Template, req.ID, state.Suffix)
	// A refresh forgets an instance that is neither enabled nor started, as the TypeScript does.
	// An import keeps it: a declaration of enabled=false, started=false is exactly that state, and
	// forgetting it would refuse the import as "does not exist".
	if err != nil || actual == nil || (!imported && !actual.enabled && !actual.started) {
		return infer.ReadResponse[SystemdInstanceArgs, SystemdInstanceState]{}, err
	}
	state.Unit, state.Enabled, state.Started = req.ID, actual.enabled, actual.started
	inputs := req.Inputs
	if imported {
		inputs = SystemdInstanceArgs{Template: state.Template, Instance: state.Instance, Enabled: &state.Enabled, Started: &state.Started, Suffix: &state.Suffix}
	}
	return infer.ReadResponse[SystemdInstanceArgs, SystemdInstanceState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the instance again.
func (SystemdInstance) Update(ctx context.Context, req infer.UpdateRequest[SystemdInstanceArgs, SystemdInstanceState]) (infer.UpdateResponse[SystemdInstanceState], error) {
	wanted := instanceWanted(req.Inputs)
	if req.DryRun {
		return infer.UpdateResponse[SystemdInstanceState]{Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SystemdInstanceState]{}, err
	}
	state, err := settleInstance(ctx, m, wanted)
	return infer.UpdateResponse[SystemdInstanceState]{Output: state}, err
}

// Diff: a different unit name replaces (delete before replace).
func (SystemdInstance) Diff(_ context.Context, req infer.DiffRequest[SystemdInstanceArgs, SystemdInstanceState]) (p.DiffResponse, error) {
	old, wanted := req.State, instanceWanted(req.Inputs)
	changed := old.Enabled != wanted.Enabled || old.Started != wanted.Started
	var replaces []string
	if old.Unit != wanted.Unit {
		replaces = []string{"template", "instance"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete disables and stops the instance; the template is not this resource's.
func (SystemdInstance) Delete(ctx context.Context, req infer.DeleteRequest[SystemdInstanceState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	unit := shellQuote(req.ID)
	state, err := m.commandResult(ctx, "systemctl show", m.escalate("systemctl show --property=LoadState --property=ActiveState -- "+unit))
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	shown := systemd.ParseShow(state.out)
	if shown["LoadState"] == "not-found" && shown["ActiveState"] == "inactive" {
		return infer.DeleteResponse{}, nil
	}
	_, err = m.commandResult(ctx, "systemctl disable --now", m.escalate("systemctl disable --now -- "+unit))
	return infer.DeleteResponse{}, err
}
