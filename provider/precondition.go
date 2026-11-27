package homelab

import (
	"context"
	"errors"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Precondition is something that must be true of the machine before the resources that depend on
// it are built. Ported from src/resources/precondition.ts.
//
// The check runs on create, update and refresh; a refresh that finds it no longer true forgets the
// resource, so the next up runs the check again and fails with the message.
type Precondition struct{}

// PreconditionArgs are the inputs of a Precondition.
type PreconditionArgs struct {
	Check   string `pulumi:"check"`
	Message string `pulumi:"message"`
	Root    *bool  `pulumi:"root,optional"`
}

// PreconditionState is what is kept in state.
type PreconditionState struct {
	Check   string `pulumi:"check"`
	Message string `pulumi:"message"`
	Root    bool   `pulumi:"root"`
}

func (a *PreconditionArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Check, "A command that exits zero when the condition holds. It must only ask, never change anything: it runs on every create, update and refresh.")
	an.Describe(&a.Message, "What to tell whoever is running the deployment when the check fails: what to do, not what went wrong.")
	an.Describe(&a.Root, "Whether the check needs root. Off by default.")
}

func (r *Precondition) Annotate(a infer.Annotator) {
	a.Describe(&r, "Something that must be true of the machine before the resources that depend on it are built.")
	a.SetToken("index", "Precondition")
}

// checkCommand is the command as it is actually sent, by create and read alike. With no
// escalation given it wraps as asRoot does (sudo -n), as the TypeScript's host-less form does.
func checkCommand(check string, root bool, escalation string) string {
	if !root {
		return check
	}
	if escalation == "" {
		return asRoot(check)
	}
	return escalate(escalation, check)
}

// readPrecondition says whether the machine satisfies it. A non-zero exit is the answer no; a
// transport failure is an error, never that answer.
func readPrecondition(ctx context.Context, m *machine, check string, root bool) (bool, error) {
	asked, err := m.ask(ctx, checkCommand(check, root, m.cfg.Escalation))
	if err != nil {
		return false, err
	}
	return asked.code == 0, nil
}

func insistPrecondition(ctx context.Context, s PreconditionState) error {
	m, err := machineFrom(ctx)
	if err != nil {
		return err
	}
	ok, err := readPrecondition(ctx, m, s.Check, s.Root)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New(s.Message)
	}
	return nil
}

func preconditionState(a PreconditionArgs) PreconditionState {
	return PreconditionState{Check: a.Check, Message: a.Message, Root: deref(a.Root)}
}

// Create asks; the check itself is the id.
func (Precondition) Create(ctx context.Context, req infer.CreateRequest[PreconditionArgs]) (infer.CreateResponse[PreconditionState], error) {
	state := preconditionState(req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[PreconditionState]{ID: state.Check, Output: state}, nil
	}
	if err := insistPrecondition(ctx, state); err != nil {
		return infer.CreateResponse[PreconditionState]{}, err
	}
	return infer.CreateResponse[PreconditionState]{ID: state.Check, Output: state}, nil
}

// Read asks again; no longer true, it is forgotten.
func (Precondition) Read(ctx context.Context, req infer.ReadRequest[PreconditionArgs, PreconditionState]) (infer.ReadResponse[PreconditionArgs, PreconditionState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[PreconditionArgs, PreconditionState]{}, err
	}
	id := req.ID
	wanted := PreconditionState{Check: id, Message: req.State.Message, Root: req.State.Root}
	// only an import, which remembers nothing, falls back to the check as the message; a message
	// declared empty stays empty, or an unchanged program would show an update
	if req.State.Check == "" {
		// An import cannot know whether the check needs root, and a root-only check run without
		// escalation reads as missing. The import ID says so with a trailing "@root"; the stored
		// ID is the check itself (the engine keeps the ID Read returns).
		if check, ok := strings.CutSuffix(id, "@root"); ok {
			id, wanted.Check, wanted.Root = check, check, true
		}
		wanted.Message = id
	}
	ok, err := readPrecondition(ctx, m, wanted.Check, wanted.Root)
	if err != nil {
		return infer.ReadResponse[PreconditionArgs, PreconditionState]{}, err
	}
	if !ok {
		return infer.ReadResponse[PreconditionArgs, PreconditionState]{}, nil
	}
	inputs := req.Inputs
	if inputs.Check == "" {
		inputs = PreconditionArgs{Check: wanted.Check, Message: wanted.Message, Root: &wanted.Root}
	}
	return infer.ReadResponse[PreconditionArgs, PreconditionState]{ID: id, Inputs: inputs, State: wanted}, nil
}

// Update asks again under the id.
func (Precondition) Update(ctx context.Context, req infer.UpdateRequest[PreconditionArgs, PreconditionState]) (infer.UpdateResponse[PreconditionState], error) {
	state := preconditionState(req.Inputs)
	state.Check = req.ID
	if req.DryRun {
		return infer.UpdateResponse[PreconditionState]{Output: state}, nil
	}
	if err := insistPrecondition(ctx, state); err != nil {
		return infer.UpdateResponse[PreconditionState]{}, err
	}
	return infer.UpdateResponse[PreconditionState]{Output: state}, nil
}

// Diff: a different question is a different precondition, asked before its dependents are touched.
func (Precondition) Diff(_ context.Context, req infer.DiffRequest[PreconditionArgs, PreconditionState]) (p.DiffResponse, error) {
	old, wanted := req.State, preconditionState(req.Inputs)
	var replaces []string
	if old.Check != wanted.Check {
		replaces = append(replaces, "check")
	}
	return diffOf(old.Message != wanted.Message || old.Root != wanted.Root, true, replaces...), nil
}

// Delete does nothing: nothing was created, and dropping a precondition means the program stopped
// caring about the condition, not that the machine should stop satisfying it.
func (Precondition) Delete(context.Context, infer.DeleteRequest[PreconditionState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
