package homelab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/line"
)

// ManagedLine is one line in a file somebody else owns, found again by its marker and placed
// relative to an anchor. Ported from src/resources/line.ts. It never creates the file, and delete
// takes out only its own line.
type ManagedLine struct{}

// ManagedLineArgs are the inputs of a ManagedLine.
type ManagedLineArgs struct {
	Path     string  `pulumi:"path"`
	Line     string  `pulumi:"line"`
	Marker   string  `pulumi:"marker"`
	Position *string `pulumi:"position,optional"`
	Anchor   *string `pulumi:"anchor,optional"`
	Comment  *string `pulumi:"comment,optional"`
}

// ManagedLineState is what is kept in state.
type ManagedLineState struct {
	Path     string `pulumi:"path"`
	Line     string `pulumi:"line"`
	Marker   string `pulumi:"marker"`
	Position string `pulumi:"position"`
	Anchor   string `pulumi:"anchor"`
	Comment  string `pulumi:"comment"`
	Actual   string `pulumi:"actual"`
	Placed   bool   `pulumi:"placed"`
}

func (a *ManagedLineArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Path, "The file the line goes in. It must exist; this never creates it.")
	an.Describe(&a.Line, "The line, without its marker.")
	an.Describe(&a.Marker, "What finds the line again, written into it as a trailing comment.")
	an.Describe(&a.Position, "prepend, append (the default), before or after the anchor.")
	an.Describe(&a.Anchor, "The text the line is placed relative to, for before and after.")
	an.Describe(&a.Comment, "The file's comment syntax, # by default.")
}

func (s *ManagedLineState) Annotate(an infer.Annotator) {
	an.Describe(&s.Actual, "The line as the file has it, marker and all.")
	an.Describe(&s.Placed, "Whether it is on the right side of its anchor.")
}

func (r *ManagedLine) Annotate(a infer.Annotator) {
	a.Describe(&r, "One line in a file somebody else owns, found again by its marker.")
	a.SetToken("index", "ManagedLine")
}

// placementRefusal refuses to place a line where it would be inert.
func placementRefusal(text, path string, position line.Position, anchor string) string {
	if position != line.Before && position != line.After {
		return ""
	}
	if anchor == "" {
		return fmt.Sprintf("position: '%s' needs an anchor to be relative to.", position)
	}
	if line.FindAnchor(text, anchor, position) < 0 {
		return fmt.Sprintf("%s does not contain %s, so the line cannot be ", path, jsonString(anchor)) +
			fmt.Sprintf("placed %s it. Appending instead would put it somewhere it may never run, ", position) +
			"which is why this refuses rather than guessing."
	}
	return ""
}

// resolveLine settles the arguments; the id is `<path>#<marker>`.
func resolveLine(a ManagedLineArgs) (ManagedLineState, string, error) {
	s := ManagedLineState{
		Path: a.Path, Line: a.Line, Marker: a.Marker,
		Position: orDefault(a.Position, string(line.Append)), Anchor: orDefault(a.Anchor, ""), Comment: orDefault(a.Comment, line.DefaultComment),
	}
	if jscompat.Trim(s.Marker) == "" {
		return s, "", fmt.Errorf("%s: a marker is what finds this line again, so it cannot be empty", a.Path)
	}
	return s, s.Path + "#" + s.Marker, nil
}

func lineArgs(s ManagedLineState) line.Args {
	return line.Args{Line: s.Line, Marker: s.Marker, Position: line.Position(s.Position), Anchor: s.Anchor, Comment: s.Comment}
}

func lineFileCommand(path string) string {
	return "test -f " + shellQuote(path) + " || exit 9; cat " + shellQuote(path)
}

func readFileLine(ctx context.Context, m *machine, path string, args line.Args) (*line.Read, error) {
	asked, err := m.ask(ctx, m.escalate(lineFileCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	read := line.ReadLine(asked.out, args)
	return &read, nil
}

func settleLine(ctx context.Context, m *machine, a ManagedLineArgs) (ManagedLineState, error) {
	wanted, _, err := resolveLine(a)
	if err != nil {
		return wanted, err
	}
	current, err := m.ask(ctx, m.escalate(lineFileCommand(wanted.Path)))
	if err != nil {
		return wanted, err
	}
	if current.code == 9 {
		return wanted, fmt.Errorf("%s does not exist on %s. This resource owns a line in a file "+
			"somebody else owns and will not create one — if the file should be this stack's, declare "+
			"it with ManagedFile instead.", wanted.Path, m.describe())
	}
	if current.code != 0 {
		return wanted, fmt.Errorf("could not read %s: %s", wanted.Path, jscompat.Trim(current.err))
	}
	if refusal := placementRefusal(current.out, wanted.Path, line.Position(wanted.Position), wanted.Anchor); refusal != "" {
		return wanted, errors.New(refusal)
	}
	updated := line.UpsertLine(current.out, lineArgs(wanted))
	if updated != current.out {
		if _, err := m.must(ctx, m.escalate(heredoc(wanted.Path, updated))); err != nil {
			return wanted, err
		}
	}
	after, err := readFileLine(ctx, m, wanted.Path, lineArgs(wanted))
	if err != nil {
		return wanted, err
	}
	if after == nil || after.Actual == "" {
		return wanted, fmt.Errorf("wrote the line into %s on %s but it is not there", wanted.Path, m.describe())
	}
	if !after.Placed {
		return wanted, fmt.Errorf("wrote the line into %s on %s but it is not %s %s — a line in the wrong place is present, correct and inert",
			wanted.Path, m.describe(), wanted.Position, jsonString(wanted.Anchor))
	}
	wanted.Actual, wanted.Placed = after.Actual, after.Placed
	return wanted, nil
}

// Create puts the line in.
func (ManagedLine) Create(ctx context.Context, req infer.CreateRequest[ManagedLineArgs]) (infer.CreateResponse[ManagedLineState], error) {
	wanted, id, err := resolveLine(req.Inputs)
	if err != nil {
		return infer.CreateResponse[ManagedLineState]{}, err
	}
	if req.DryRun {
		return infer.CreateResponse[ManagedLineState]{ID: id, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[ManagedLineState]{}, err
	}
	state, err := settleLine(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[ManagedLineState]{}, err
	}
	return infer.CreateResponse[ManagedLineState]{ID: id, Output: state}, nil
}

// unmarkLine splits a marked line back into the line and the comment syntax MarkLine joined:
// `<line> <comment> pulumi-homelab:<marker>`.
func unmarkLine(actual, marker string) (text, comment string, ok bool) {
	suffix := " " + line.MarkerOf(marker)
	if !strings.HasSuffix(actual, suffix) {
		return "", "", false
	}
	before := strings.TrimSuffix(actual, suffix)
	at := strings.LastIndex(before, " ")
	if at < 0 {
		return "", "", false
	}
	text, comment = before[:at], before[at+1:]
	if comment == "" || line.MarkLine(text, marker, comment) != actual {
		return "", "", false
	}
	return text, comment, true
}

func idParts(id string) (string, string) {
	parts := strings.Split(id, "#")
	return parts[0], strings.Join(parts[1:], "#")
}

// Read reports the line as the file has it; the file gone, it is forgotten.
func (ManagedLine) Read(ctx context.Context, req infer.ReadRequest[ManagedLineArgs, ManagedLineState]) (infer.ReadResponse[ManagedLineArgs, ManagedLineState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[ManagedLineArgs, ManagedLineState]{}, err
	}
	state := req.State
	imported := state.Path == "" && state.Marker == ""
	path, marker := idParts(req.ID)
	if imported {
		state.Path, state.Marker = path, marker
		state.Position, state.Comment = string(line.Append), line.DefaultComment
	}
	found, err := readFileLine(ctx, m, state.Path, lineArgs(state))
	if err != nil || found == nil {
		return infer.ReadResponse[ManagedLineArgs, ManagedLineState]{}, err
	}
	state.Actual, state.Placed = found.Actual, found.Placed
	if imported {
		// nothing remembered: the declared text and comment syntax are recovered from the marked line
		// itself, so the imported inputs reproduce it exactly and diff as no change
		if text, comment, ok := unmarkLine(found.Actual, state.Marker); ok {
			state.Line, state.Comment = text, comment
		}
	}
	inputs := req.Inputs
	if inputs.Path == "" {
		inputs = ManagedLineArgs{Path: state.Path, Line: state.Line, Marker: state.Marker, Position: &state.Position, Anchor: &state.Anchor, Comment: &state.Comment}
	}
	return infer.ReadResponse[ManagedLineArgs, ManagedLineState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the line again.
func (ManagedLine) Update(ctx context.Context, req infer.UpdateRequest[ManagedLineArgs, ManagedLineState]) (infer.UpdateResponse[ManagedLineState], error) {
	if req.DryRun {
		wanted, _, err := resolveLine(req.Inputs)
		return infer.UpdateResponse[ManagedLineState]{Output: wanted}, err
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[ManagedLineState]{}, err
	}
	state, err := settleLine(ctx, m, req.Inputs)
	return infer.UpdateResponse[ManagedLineState]{Output: state}, err
}

// Diff: a hand edit, a line moved past its anchor or a new placement is a change; a different file
// or marker is a different line (delete before replace).
func (ManagedLine) Diff(_ context.Context, req infer.DiffRequest[ManagedLineArgs, ManagedLineState]) (p.DiffResponse, error) {
	old := req.State
	wanted, _, err := resolveLine(req.Inputs)
	if err != nil {
		return p.DiffResponse{}, err
	}
	changed := old.Actual != line.MarkLine(wanted.Line, wanted.Marker, wanted.Comment) ||
		!old.Placed || old.Position != wanted.Position || old.Anchor != wanted.Anchor
	var replaces []string
	if old.Path != wanted.Path || old.Marker != wanted.Marker {
		replaces = []string{"path", "marker"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete takes out only its own line; a file already gone is not a failure.
func (ManagedLine) Delete(ctx context.Context, req infer.DeleteRequest[ManagedLineState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	path, marker := idParts(req.ID)
	if req.State.Path != "" {
		path = req.State.Path
	}
	if req.State.Marker != "" {
		marker = req.State.Marker
	}
	current, err := m.ask(ctx, m.escalate(lineFileCommand(path)))
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if current.code == 9 {
		return infer.DeleteResponse{}, nil
	}
	if current.code != 0 {
		return infer.DeleteResponse{}, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(current.err))
	}
	if updated := line.RemoveLine(current.out, marker); updated != current.out {
		_, err = m.must(ctx, m.escalate(heredoc(path, updated)))
	}
	return infer.DeleteResponse{}, err
}
