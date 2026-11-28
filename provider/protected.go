package homelab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/shell"
	"github.com/antimatter-studios/pulumi-homelab/provider/stat"
)

// ProtectedFile is a file on the machine whose content this provider never holds. Ported from
// src/resources/protected.ts.
//
// It takes coordinates instead of content: the inputs say where to fetch the material from, the
// provider fetches it locally at apply time, writes it to the machine as stdin data, and forgets
// it. What lands in state is the source, a mode, an owner and a SHA-256 digest, none of which is a
// secret, and the last of which is what makes drift detectable anyway.
type ProtectedFile struct{}

// Source is where the material comes from, as data rather than as a function: it can only ever name
// a source, never hold a value. It names something the deploying machine can run.
//
//   - exec: run argv; the material is its stdout (op read, pass, gpg -d, vault kv get).
//   - execFile: run argv with {} replaced by a private temporary file; the material is what it
//     wrote there (trove get file --out {}).
//   - file: a file on the deploying machine.
type Source struct {
	Kind string   `pulumi:"kind" json:"kind"`
	Argv []string `pulumi:"argv,optional" json:"argv,omitempty"`
	Path *string  `pulumi:"path,optional" json:"path,omitempty"`
}

func (s *Source) Annotate(a infer.Annotator) {
	a.Describe(&s.Kind, "exec (the material is stdout), execFile (the material is what the command wrote to {}) or file (a file on the deploying machine).")
	a.Describe(&s.Argv, "For exec and execFile: the program and its arguments, run without a shell. execFile needs a {} somewhere in it.")
	a.Describe(&s.Path, "For file: the path on the deploying machine.")
}

// ProtectedFileArgs are the inputs of a ProtectedFile.
type ProtectedFileArgs struct {
	Path    string  `pulumi:"path"`
	Source  Source  `pulumi:"source"`
	Mode    *string `pulumi:"mode,optional"`
	Owner   *string `pulumi:"owner,optional"`
	Group   *string `pulumi:"group,optional"`
	Enforce *string `pulumi:"enforce,optional"`
}

// ProtectedFileState is what is kept in state. Nothing in it is the content.
type ProtectedFileState struct {
	Path          string  `pulumi:"path"`
	Source        Source  `pulumi:"source"`
	Mode          string  `pulumi:"mode"`
	Owner         string  `pulumi:"owner"`
	Group         string  `pulumi:"group"`
	Enforce       string  `pulumi:"enforce"`
	Digest        string  `pulumi:"digest"`
	WrittenDigest *string `pulumi:"writtenDigest,optional" provider:"secret"`
}

func (f *ProtectedFileArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.Path, "Absolute path of the file on the machine. A new path is a new secret.")
	a.Describe(&f.Source, "Where the content comes from. Not the content.")
	a.Describe(&f.Mode, "Octal: '0600'.")
	a.SetDefault(&f.Mode, protectedDefaults.mode)
	a.Describe(&f.Owner, "Owning user.")
	a.SetDefault(&f.Owner, protectedDefaults.owner)
	a.Describe(&f.Group, "Owning group.")
	a.SetDefault(&f.Group, protectedDefaults.group)
	a.Describe(&f.Enforce, "always: keep the file as the source says, and report a hand edit or a rotated source as drift. once: write it when absent and never rewrite it, for material the application itself rotates.")
	a.SetDefault(&f.Enforce, protectedDefaults.enforce)
}

func (f *ProtectedFileState) Annotate(a infer.Annotator) {
	a.Describe(&f.Digest, "SHA-256 of the content under always, and the word \"seeded\" under once.")
	a.Describe(&f.WrittenDigest, "SHA-256 of the bytes last written by the provider, used to guard deletion.")
}

func (f *ProtectedFile) Annotate(a infer.Annotator) {
	a.Describe(&f, "A secret file on the machine, described by where it comes from rather than by what it says.")
	a.SetToken("index", "ProtectedFile")
}

func (ProtectedFile) WireDependencies(f infer.FieldSelector, _ *ProtectedFileArgs, state *ProtectedFileState) {
	f.OutputField(&state.WrittenDigest).AlwaysSecret()
}

var protectedDefaults = struct{ mode, owner, group, enforce string }{"0600", "root", "root", "once"}

// seeded is what digest says when the content is deliberately not being tracked. A word rather
// than an empty string, which would read as "not computed yet" and invite a comparison against one.
const seeded = "seeded"

// substituteArgv replaces {} with the temporary file in every argument that mentions it, inside an
// argument as well as as a whole one. No quoting: an argv goes through no shell.
func substituteArgv(argv []string, where string) []string {
	out := make([]string, len(argv))
	for i, arg := range argv {
		out[i] = strings.ReplaceAll(arg, "{}", where)
	}
	return out
}

// sourceRefusal says why a source cannot work, before anything runs, or "" when it can.
//
// An empty argv would fail as "no program", naming neither the resource nor the field. An execFile
// with no {} is worse: the command runs, writes nothing where the provider looks, and the result is
// an empty secret written successfully.
func sourceRefusal(source Source) string {
	if source.Kind == "file" {
		if deref(source.Path) == "" {
			return "source.path is empty"
		}
		return ""
	}
	if len(source.Argv) == 0 {
		return fmt.Sprintf("source.argv is empty for kind '%s'", source.Kind)
	}
	if source.Kind == "execFile" {
		for _, arg := range source.Argv {
			if strings.Contains(arg, "{}") {
				return ""
			}
		}
		return "source.argv for kind 'execFile' has no '{}' for the provider to substitute"
	}
	return ""
}

// needsMaterial says whether the material has to be fetched to answer the question asked. Under
// once it never does, and that is not an optimisation: fetching would unlock a vault, run a
// subprocess and hold a secret in memory for an answer that is discarded, during a preview.
func needsMaterial(enforce string) bool {
	return enforce == "always"
}

// collectTimeout bounds a source command, as execFileSync's timeout did.
const collectTimeout = 30 * time.Second

// collect takes the material from wherever it lives, locally.
func collect(ctx context.Context, source Source) ([]byte, error) {
	if refusal := sourceRefusal(source); refusal != "" {
		return nil, errors.New("ProtectedFile: " + refusal)
	}
	if source.Kind == "file" {
		return os.ReadFile(*source.Path)
	}
	ctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()
	run := func(argv []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil {
			// Source commands may print secret material or include it in their arguments, so the
			// error names the program only, never its arguments or its output.
			name := filepath.Base(argv[0])
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil, fmt.Errorf("ProtectedFile: local source command %q failed (%s)", name, exit.ProcessState)
			}
			return nil, fmt.Errorf("ProtectedFile: could not start local source command %q", name)
		}
		return stdout.Bytes(), nil
	}
	if source.Kind == "exec" {
		return run(source.Argv)
	}
	// execFile: a directory only this user can enter, removed straight away, so the bytes are on
	// disk for the moment between those two steps and nowhere else
	dir, err := os.MkdirTemp("", "protected-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "blob")
	if _, err := run(substituteArgv(source.Argv, out)); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}

func wantedProtectedOf(args ProtectedFileArgs) ProtectedFileState {
	w := ProtectedFileState{Path: args.Path, Source: args.Source, Mode: protectedDefaults.mode,
		Owner: protectedDefaults.owner, Group: protectedDefaults.group, Enforce: protectedDefaults.enforce}
	if args.Mode != nil {
		w.Mode = *args.Mode
	}
	if args.Owner != nil {
		w.Owner = *args.Owner
	}
	if args.Group != nil {
		w.Group = *args.Group
	}
	if args.Enforce != nil {
		w.Enforce = *args.Enforce
	}
	return w
}

// writeProtectedCommand streams the bytes into the target under the TypeScript umask.
func writeProtectedCommand(s ProtectedFileState) string {
	q := shell.Quote(s.Path)
	return fmt.Sprintf("set -e; install -d -m 0755 %s; umask 077; cat > %s; %s",
		shell.Quote(parentOf(s.Path)), q, protectedAttributes(s))
}

func protectedAttributes(s ProtectedFileState) string {
	q := shell.Quote(s.Path)
	return fmt.Sprintf("chmod %s %s; chown %s %s", shell.Quote(s.Mode), q, shell.Quote(s.Owner+":"+s.Group), q)
}

// refaceCommand sets the mode and ownership alone, for when the content is not ours to rewrite.
func refaceCommand(s ProtectedFileState) string {
	return "set -e; " + protectedAttributes(s)
}

// readProtectedCommand asks whether the file is there, its digest under always, and its stat line.
func readProtectedCommand(path, enforce string) string {
	q := shell.Quote(path)
	digest := ""
	if needsMaterial(enforce) {
		digest = fmt.Sprintf("sha256sum %s | cut -d' ' -f1; ", q)
	}
	return fmt.Sprintf("test -f %s || exit 9; %sstat -c '%%a %%U %%G' %s", q, digest, q)
}

// Create fetches the material and writes it. A preview fetches nothing.
func (ProtectedFile) Create(ctx context.Context, req infer.CreateRequest[ProtectedFileArgs]) (infer.CreateResponse[ProtectedFileState], error) {
	w := wantedProtectedOf(req.Inputs)
	if req.DryRun {
		if !needsMaterial(w.Enforce) {
			w.Digest = seeded
		}
		return infer.CreateResponse[ProtectedFileState]{ID: w.Path, Output: w}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[ProtectedFileState]{}, err
	}
	content, err := collect(ctx, w.Source)
	if err != nil {
		return infer.CreateResponse[ProtectedFileState]{}, err
	}
	digest := digestOf(content)
	w.WrittenDigest = &digest
	w.Digest = seeded
	if needsMaterial(w.Enforce) {
		w.Digest = digest
	}
	if _, err := m.feed(ctx, writeProtectedCommand(w), content); err != nil {
		return infer.CreateResponse[ProtectedFileState]{}, err
	}
	return infer.CreateResponse[ProtectedFileState]{ID: w.Path, Output: w}, nil
}

// Read reports the file's digest (under always) and its mode and ownership. An absent file is an
// absent resource, which under once is precisely what makes it seed again.
func (ProtectedFile) Read(ctx context.Context, req infer.ReadRequest[ProtectedFileArgs, ProtectedFileState]) (infer.ReadResponse[ProtectedFileArgs, ProtectedFileState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[ProtectedFileArgs, ProtectedFileState]{}, err
	}
	state := req.State
	if state.Source.Kind == "" {
		state.Source = req.Inputs.Source
	}
	if state.Enforce == "" {
		state.Enforce = deref(req.Inputs.Enforce)
	}
	probeEnforce := state.Enforce
	if probeEnforce == "" {
		// A bare import cannot recover the declaration. Record the digest so either
		// enforcement can be compared on the first preview without rewriting the file.
		probeEnforce = "always"
	}
	asked, err := m.ask(ctx, m.escalate(readProtectedCommand(req.ID, probeEnforce)))
	if err != nil {
		return infer.ReadResponse[ProtectedFileArgs, ProtectedFileState]{}, err
	}
	if asked.code == 9 {
		return infer.ReadResponse[ProtectedFileArgs, ProtectedFileState]{}, nil
	}
	if asked.code != 0 {
		return infer.ReadResponse[ProtectedFileArgs, ProtectedFileState]{}, fmt.Errorf("could not read %s: %s", req.ID, jscompat.Trim(asked.err))
	}
	lines := strings.Split(jscompat.Trim(asked.out), "\n")
	state.Digest = seeded
	if needsMaterial(probeEnforce) {
		state.Digest, lines = lines[0], lines[1:]
	}
	statLine := ""
	if len(lines) > 0 {
		statLine = lines[0]
	}
	found := stat.ParseFileStat(statLine)
	state.Path, state.Mode, state.Owner, state.Group = req.ID, found.Mode, found.Owner, found.Group
	inputs := req.Inputs
	if inputs.Path == "" {
		inputs = ProtectedFileArgs{Path: req.ID, Source: state.Source, Mode: &state.Mode, Owner: &state.Owner, Group: &state.Group}
		if state.Enforce != "" {
			inputs.Enforce = &state.Enforce
		}
	}
	return infer.ReadResponse[ProtectedFileArgs, ProtectedFileState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update under once only sets mode and ownership: the content belongs to whatever rewrites it now,
// and rewriting would undo a rotation, so the material is not even fetched. Under always it
// fetches and rewrites.
func (ProtectedFile) Update(ctx context.Context, req infer.UpdateRequest[ProtectedFileArgs, ProtectedFileState]) (infer.UpdateResponse[ProtectedFileState], error) {
	w := wantedProtectedOf(req.Inputs)
	w.Path = req.ID
	w.WrittenDigest = req.State.WrittenDigest
	if !needsMaterial(w.Enforce) {
		w.Digest = seeded
	}
	if req.DryRun {
		return infer.UpdateResponse[ProtectedFileState]{Output: w}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[ProtectedFileState]{}, err
	}
	if !needsMaterial(w.Enforce) {
		_, err := m.must(ctx, m.escalate(refaceCommand(w)))
		return infer.UpdateResponse[ProtectedFileState]{Output: w}, err
	}
	content, err := collect(ctx, w.Source)
	if err != nil {
		return infer.UpdateResponse[ProtectedFileState]{}, err
	}
	w.Digest = digestOf(content)
	w.WrittenDigest = &w.Digest
	if _, err := m.feed(ctx, writeProtectedCommand(w), content); err != nil {
		return infer.UpdateResponse[ProtectedFileState]{}, err
	}
	return infer.UpdateResponse[ProtectedFileState]{Output: w}, nil
}

// Diff asks the source what it holds now under always, which is the whole of drift detection, and
// never under once. A new path is a new secret, replaced with the old one deleted first.
func (ProtectedFile) Diff(ctx context.Context, req infer.DiffRequest[ProtectedFileArgs, ProtectedFileState]) (p.DiffResponse, error) {
	w := wantedProtectedOf(req.Inputs)
	old := req.State
	diff := map[string]p.PropertyDiff{}
	for name, changed := range map[string]bool{
		"mode": old.Mode != w.Mode, "owner": old.Owner != w.Owner, "group": old.Group != w.Group,
		"enforce": old.Enforce != "" && old.Enforce != w.Enforce,
	} {
		if changed {
			diff[name] = p.PropertyDiff{Kind: p.Update}
		}
	}
	if needsMaterial(w.Enforce) {
		content, err := collect(ctx, w.Source)
		if err != nil {
			return p.DiffResponse{}, err
		}
		if old.Digest != digestOf(content) {
			diff["digest"] = p.PropertyDiff{Kind: p.Update}
		}
	}
	if old.Path != w.Path {
		diff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff, DeleteBeforeReplace: true}, nil
}

// Delete removes the file, and nothing else.
func (ProtectedFile) Delete(ctx context.Context, req infer.DeleteRequest[ProtectedFileState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = removeWrittenFile(ctx, m, req.ID, req.State.WrittenDigest)
	return infer.DeleteResponse{}, err
}
