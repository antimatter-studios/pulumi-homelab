package homelab

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Symlink is a symbolic link, and a read that can tell four things apart. Ported from
// src/resources/symlink.ts.
//
// A link pointing where the code says, a link pointing somewhere else, something real at that path,
// and nothing at all. Something real where a link belongs is refused, naming what is there: it
// would be easy to replace it and it must not, because whatever is in it is somebody's data.
type Symlink struct{}

// SymlinkArgs are the inputs of a Symlink.
type SymlinkArgs struct {
	Path                 string `pulumi:"path"`
	Target               string `pulumi:"target"`
	AcceptLauncherScript *bool  `pulumi:"acceptLauncherScript,optional"`
}

// SymlinkState is what is kept in state.
type SymlinkState struct {
	Path                 string `pulumi:"path"`
	Target               string `pulumi:"target"`
	AcceptLauncherScript *bool  `pulumi:"acceptLauncherScript,optional"`
}

func (s *SymlinkArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Path, "Where the link itself lives.")
	a.Describe(&s.Target, "What it points at. Not required to exist: a link to a mount point that is not up yet is normal.")
	a.Describe(&s.AcceptLauncherScript, "Accept a launcher script in place of the link: a small `#!/bin/sh` file whose job is to "+
		"`exec '<program>'`. Its program is read back as the target. For software that updates itself by rewriting its own "+
		"launcher. Only a script that small and that shape is accepted; anything else is still refused as data.")
}

func (s *Symlink) Annotate(a infer.Annotator) {
	a.Describe(&s, "A symlink that is checked for what it actually is, not merely whether the path exists.")
	a.SetToken("index", "Symlink")
}

// launcherExec is /^\s*exec\s+(?:'([^']+)'|"([^"]+)"|(\S+))/ with JavaScript's \s and \S; the m
// flag is applied by trying it at every line start, as JavaScript's ^ does.
var launcherExec = regexp.MustCompile(`^` + jscompat.SpaceClass + `*exec` + jscompat.SpaceClass +
	`+(?:'([^']+)'|"([^"]+)"|(` + jscompat.NonSpaceClass + `+))`)

// launcherTarget is the program a launcher script execs, or "" when the text is not one.
func launcherTarget(text string) string {
	if !strings.HasPrefix(text, "#!") || len(utf16.Encode([]rune(text))) > 4096 {
		return ""
	}
	// JavaScript's multiline ^ matches at the start and after \n, \r, U+2028 and U+2029
	starts := []int{0}
	for i, r := range text {
		if r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 {
			starts = append(starts, i+len(string(r)))
		}
	}
	for _, at := range starts {
		m := launcherExec.FindStringSubmatch(text[at:])
		if m == nil {
			continue
		}
		for _, g := range m[1:] {
			if g != "" {
				return g
			}
		}
	}
	return ""
}

// symlinkFound is what an answer from the machine means: "link" with its target, "absent", or
// "occupied" naming the kind of thing in the way.
type symlinkFound struct {
	State  string
	Target string
	Kind   string
}

// interpretSymlink: 9 is nothing there, 8 is something real in the way, anything else a link.
func interpretSymlink(code int, out string) symlinkFound {
	switch code {
	case 9:
		return symlinkFound{State: "absent"}
	case 8:
		kind := jscompat.Trim(out)
		if kind == "" {
			kind = "something else"
		}
		return symlinkFound{State: "occupied", Kind: kind}
	}
	return symlinkFound{State: "link", Target: out}
}

// readSymlink is what is at that path: the link's target, or nil where there is nothing. Something
// real in the way is an error, because every automatic answer to it is worse than stopping.
func readSymlink(ctx context.Context, m *machine, path string, acceptLauncherScript bool) (*SymlinkState, error) {
	quoted := shellQuote(path)
	asked, err := m.ask(ctx, m.escalate(
		// readlink succeeds for a broken link too, which is right: a link pointing at a filesystem
		// that is not mounted yet is the ordinary case here
		"if pointsAt=$(readlink "+quoted+" 2>/dev/null); then printf '%s' \"$pointsAt\"; exit 0; fi; "+
			"if [ -e "+quoted+" ] || [ -L "+quoted+" ]; then stat -c '%F' "+quoted+"; exit 8; fi; "+
			"exit 9"))
	if err != nil {
		return nil, err
	}
	if asked.code != 0 && asked.code != 8 && asked.code != 9 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	found := interpretSymlink(asked.code, asked.out)
	if found.State == "absent" {
		return nil, nil
	}
	if found.State == "occupied" && acceptLauncherScript && found.Kind == "regular file" {
		script, err := m.ask(ctx, m.escalate("head -c 4097 "+quoted))
		if err != nil {
			return nil, err
		}
		if script.code == 0 {
			if target := launcherTarget(script.out); target != "" {
				return &SymlinkState{Path: path, Target: target}, nil
			}
		}
	}
	if found.State == "occupied" {
		return nil, fmt.Errorf("%s on %s is a %s, not a symlink. "+
			"Refusing to replace it: whatever is in there is data this resource did not put there.",
			path, m.describe(), found.Kind)
	}
	return &SymlinkState{Path: path, Target: found.Target}, nil
}

// symlinkParent is path.replace(/\/[^/]*$/, ”) || '/'.
func symlinkParent(path string) string {
	parent := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		parent = path[:i]
	}
	if parent == "" {
		return "/"
	}
	return parent
}

func link(ctx context.Context, m *machine, args SymlinkArgs) error {
	// readSymlink refuses anything real in the way, which is the check that has to happen before
	// -f is allowed anywhere near the path
	current, err := readSymlink(ctx, m, args.Path, deref(args.AcceptLauncherScript))
	if err != nil {
		return err
	}
	// already pointing where it should
	if current != nil && current.Target == args.Target {
		return nil
	}
	// a launcher script the software wrote for itself is never replaced, only accepted
	if current != nil && deref(args.AcceptLauncherScript) {
		return nil
	}
	// -n as well as -f: without it, relinking a link that points at a directory creates the new link
	// inside that directory instead of replacing it
	_, err = m.must(ctx, m.escalate("mkdir -p "+shellQuote(symlinkParent(args.Path))+" && "+
		"ln -sfn "+shellQuote(args.Target)+" "+shellQuote(args.Path)))
	return err
}

func symlinkStateOf(args SymlinkArgs) SymlinkState {
	return SymlinkState{Path: args.Path, Target: args.Target, AcceptLauncherScript: args.AcceptLauncherScript}
}

// Create links the path, adopting a link that already points where it should.
func (Symlink) Create(ctx context.Context, req infer.CreateRequest[SymlinkArgs]) (infer.CreateResponse[SymlinkState], error) {
	if req.DryRun {
		return infer.CreateResponse[SymlinkState]{ID: req.Inputs.Path, Output: symlinkStateOf(req.Inputs)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SymlinkState]{}, err
	}
	if err := link(ctx, m, req.Inputs); err != nil {
		return infer.CreateResponse[SymlinkState]{}, err
	}
	return infer.CreateResponse[SymlinkState]{ID: req.Inputs.Path, Output: symlinkStateOf(req.Inputs)}, nil
}

// Read reports the link's target; a link gone from the machine is forgotten, and something real in
// its place is an error. On import, the inputs are what the machine says.
func (Symlink) Read(ctx context.Context, req infer.ReadRequest[SymlinkArgs, SymlinkState]) (infer.ReadResponse[SymlinkArgs, SymlinkState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SymlinkArgs, SymlinkState]{}, err
	}
	accept := req.State.AcceptLauncherScript
	if accept == nil {
		accept = req.Inputs.AcceptLauncherScript
	}
	// An import has only the id, so a launcher script in place of the link would read as a file in
	// the way. The id says the declaration accepts one with an @launcher suffix (path@launcher).
	id := req.ID
	importing := req.State.Path == "" && req.Inputs.Path == ""
	if path, ok := strings.CutSuffix(id, "@launcher"); ok && importing {
		yes := true
		id, accept = path, &yes
	}
	actual, err := readSymlink(ctx, m, id, deref(accept))
	if err != nil {
		return infer.ReadResponse[SymlinkArgs, SymlinkState]{}, err
	}
	if actual == nil {
		return infer.ReadResponse[SymlinkArgs, SymlinkState]{}, nil
	}
	state := req.State
	state.Path, state.Target = actual.Path, actual.Target
	if importing {
		state.AcceptLauncherScript = accept
	}
	inputs := req.Inputs
	if inputs.Path == "" {
		inputs = SymlinkArgs{Path: actual.Path, Target: actual.Target, AcceptLauncherScript: accept}
	}
	return infer.ReadResponse[SymlinkArgs, SymlinkState]{ID: id, Inputs: inputs, State: state}, nil
}

// Update relinks under the id; a path change is a replacement, never an update.
func (Symlink) Update(ctx context.Context, req infer.UpdateRequest[SymlinkArgs, SymlinkState]) (infer.UpdateResponse[SymlinkState], error) {
	wanted := req.Inputs
	wanted.Path = req.ID
	if req.DryRun {
		return infer.UpdateResponse[SymlinkState]{Output: symlinkStateOf(wanted)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SymlinkState]{}, err
	}
	if err := link(ctx, m, wanted); err != nil {
		return infer.UpdateResponse[SymlinkState]{}, err
	}
	return infer.UpdateResponse[SymlinkState]{Output: symlinkStateOf(wanted)}, nil
}

// Diff: a new target is an update; a new path replaces, deleting the old link first.
func (Symlink) Diff(_ context.Context, req infer.DiffRequest[SymlinkArgs, SymlinkState]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if req.State.Target != req.Inputs.Target {
		resp.DetailedDiff["target"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Path != req.Inputs.Path {
		resp.DetailedDiff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// symlinkDeleteCommand captures the directory entry with a same-directory rename.
// Checking the captured entry avoids a type check followed by an unlink at the live path.
func symlinkDeleteCommand(file string, target *string) string {
	check := `[ -L "$staging/entry" ]`
	if target != nil {
		check += ` && [ "$(readlink -- "$staging/entry")" = ` + shellQuote(*target) + " ]"
	}
	return "set -e; [ -L " + shellQuote(file) + " ] || exit 0; cd -- " + shellQuote(path.Dir(file)) + "; entry=" + shellQuote("./"+path.Base(file)) + `
[ -L "$entry" ] || exit 0
staging=$(mktemp -d ./.pulumi-symlink.XXXXXXXX)
trap 'rmdir -- "$staging" 2>/dev/null || true' EXIT
if ! mv -T -- "$entry" "$staging/entry"; then
    if [ ! -e "$entry" ] && [ ! -L "$entry" ]; then exit 0; fi
    exit 1
fi
if ` + check + `; then
    rm -- "$staging/entry"
    exit 0
fi
# The entry changed before the rename. Put it back without overwriting a concurrent arrival.
if ! mv -nT -- "$staging/entry" "$entry" || [ -e "$staging/entry" ] || [ -L "$staging/entry" ]; then
    printf 'The path changed during deletion. The captured entry was kept at %s/%s/entry.\n' "$PWD" "$staging" >&2
else
    printf 'The path changed during deletion. The captured entry was restored.\n' >&2
fi
exit 1`
}

// Delete removes the path only while it is still a link: if something replaced it with a real
// directory since the last deployment, removing the resource must not be what deletes it.
func (Symlink) Delete(ctx context.Context, req infer.DeleteRequest[SymlinkState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(symlinkDeleteCommand(req.ID, nil)))
	return infer.DeleteResponse{}, err
}
