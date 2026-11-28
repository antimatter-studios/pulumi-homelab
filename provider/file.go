package homelab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/shell"
	"github.com/antimatter-studios/pulumi-homelab/provider/stat"
)

// ManagedFile is a file on the machine with its content, owner and mode. Ported from
// src/resources/file.ts.
//
// Refresh reports drift from the machine. Content travels on stdin and the output holds its
// SHA-256 digest.
type ManagedFile struct{}

// ManagedFileArgs are the inputs of a ManagedFile.
type ManagedFileArgs struct {
	Path          string  `pulumi:"path"`
	Content       string  `pulumi:"content"`
	Mode          *string `pulumi:"mode,optional"`
	Owner         *string `pulumi:"owner,optional"`
	Group         *string `pulumi:"group,optional"`
	ReloadSystemd *bool   `pulumi:"reloadSystemd,optional"`
}

// ManagedFileState is what is kept in state: the facts about the file, and the digest of its
// content in place of the content.
type ManagedFileState struct {
	Path  string `pulumi:"path"`
	Mode  string `pulumi:"mode"`
	Owner string `pulumi:"owner"`
	Group string `pulumi:"group"`
	// ReloadSystemd is nil after an import: it is a behaviour, not a fact the file shows, so an
	// imported file cannot know it and Diff does not compare it until a write records it.
	ReloadSystemd *bool  `pulumi:"reloadSystemd,optional"`
	Digest        string `pulumi:"digest"`
}

func (f *ManagedFileArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.Path, "Absolute path of the file on the machine. A new path is a new file.")
	a.Describe(&f.Content, "The file's content. A missing final newline is added, as a here-document would.")
	a.Describe(&f.Mode, "Octal, as it is written everywhere else: '0644'.")
	a.SetDefault(&f.Mode, managedFileDefaults.mode)
	a.Describe(&f.Owner, "Owning user.")
	a.SetDefault(&f.Owner, managedFileDefaults.owner)
	a.Describe(&f.Group, "Owning group.")
	a.SetDefault(&f.Group, managedFileDefaults.group)
	a.Describe(&f.ReloadSystemd, "Run `systemctl daemon-reload` after writing: for a static unit, one SystemdUnit cannot describe.")
	a.SetDefault(&f.ReloadSystemd, false)
}

func (f *ManagedFileState) Annotate(a infer.Annotator) {
	a.Describe(&f.Digest, "SHA-256 in hex of the declared content after a write or the observed content after a read.")
}

func (f *ManagedFile) Annotate(a infer.Annotator) {
	a.Describe(&f, "A file that is kept as the code says it should be, and noticed when it is not.")
	a.SetToken("index", "ManagedFile")
}

var managedFileDefaults = struct{ mode, owner, group string }{"0644", "root", "root"}

// wantedFile is a ManagedFile's inputs with the defaults applied.
type wantedFile struct {
	path, content, mode, owner, group string
	reloadSystemd                     bool
}

func wantedOf(args ManagedFileArgs) wantedFile {
	w := wantedFile{path: args.Path, content: args.Content, mode: managedFileDefaults.mode,
		owner: managedFileDefaults.owner, group: managedFileDefaults.group, reloadSystemd: deref(args.ReloadSystemd)}
	if args.Mode != nil {
		w.mode = *args.Mode
	}
	if args.Owner != nil {
		w.owner = *args.Owner
	}
	if args.Group != nil {
		w.group = *args.Group
	}
	return w
}

// fileBody is what lands on disk: the content, ending in a newline. The TypeScript wrote through a
// here-document, whose closing delimiter has to stand on its own line, and gave a big file the same
// newline so it read back identically; without the same rule every file would diff on the switch.
func fileBody(content string) string {
	if strings.HasSuffix(content, "\n") {
		return content
	}
	return content + "\n"
}

// digestOf is the SHA-256 of some bytes, in lower-case hex as sha256sum prints it.
func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// writeFileCommand keeps the TypeScript cutoff between direct and staged writes. Streaming
// the body replaces both the small heredoc and the large base64 upload.
func writeFileCommand(w wantedFile) string {
	const inlineBytes = 64 * 1024
	path := shell.Quote(w.path)
	directory := parentOf(w.path)
	parent := shell.Quote(directory)
	reload := ""
	if w.reloadSystemd {
		reload = " && systemctl daemon-reload"
	}
	attributes := func(target string) string {
		return fmt.Sprintf("chmod %s %s && chown %s %s",
			shell.Quote(w.mode), target, shell.Quote(w.owner+":"+w.group), target)
	}
	if len(w.content) <= inlineBytes {
		return fmt.Sprintf("mkdir -p %s && cat > %s\n%s%s", parent, path, attributes(path), reload)
	}
	template := shell.Quote(strings.TrimSuffix(directory, "/") + "/.pulumi-file.XXXXXX")
	const stage = `mkdir -p %s && staged=$(mktemp -- %s) &&
trap 'rm -f -- "$staged"' EXIT &&
cat > "$staged" && %s && mv -fT -- "$staged" %s%s`
	return fmt.Sprintf(stage, parent, template, attributes(`"$staged"`), path, reload)
}

// writeManagedFile puts the file there, exactly as described.
func writeManagedFile(ctx context.Context, m *machine, w wantedFile) error {
	_, err := m.feed(ctx, writeFileCommand(w), []byte(fileBody(w.content)))
	return err
}

// actualFile is what the machine says is at a path.
type actualFile struct {
	stat.FileStat
	Content string
}

// readFileCommand asks all four questions in one round trip, since a handshake costs far more than
// the work: exit 9 is "no such file", said by the test rather than inferred from a message.
func readFileCommand(path string) string {
	q := shell.Quote(path)
	return fmt.Sprintf("test -f %s || exit 9; stat -c '%%a %%U %%G' %s && cat %s", q, q, q)
}

// readManagedFile reports what the machine has at path, or nil where there is no such file.
func readManagedFile(ctx context.Context, m *machine, path string) (*actualFile, error) {
	asked, err := m.ask(ctx, m.escalate(readFileCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	first, rest, _ := strings.Cut(asked.out, "\n")
	return &actualFile{FileStat: stat.ParseFileStat(first), Content: rest}, nil
}

func (w wantedFile) state() ManagedFileState {
	return ManagedFileState{Path: w.path, Mode: w.mode, Owner: w.owner, Group: w.group,
		ReloadSystemd: &w.reloadSystemd, Digest: digestOf([]byte(w.content))}
}

// Create writes the file.
func (ManagedFile) Create(ctx context.Context, req infer.CreateRequest[ManagedFileArgs]) (infer.CreateResponse[ManagedFileState], error) {
	w := wantedOf(req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[ManagedFileState]{ID: w.path, Output: w.state()}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[ManagedFileState]{}, err
	}
	if err := writeManagedFile(ctx, m, w); err != nil {
		return infer.CreateResponse[ManagedFileState]{}, err
	}
	return infer.CreateResponse[ManagedFileState]{ID: w.path, Output: w.state()}, nil
}

// Read reports what the machine has. A file gone from the machine is forgotten, and the next up
// recreates it. reloadSystemd is a behaviour rather than a fact about the file, so it is carried
// over from the state. An import, which has no inputs, takes them from the file as it is.
func (ManagedFile) Read(ctx context.Context, req infer.ReadRequest[ManagedFileArgs, ManagedFileState]) (infer.ReadResponse[ManagedFileArgs, ManagedFileState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[ManagedFileArgs, ManagedFileState]{}, err
	}
	actual, err := readManagedFile(ctx, m, req.ID)
	if err != nil || actual == nil {
		return infer.ReadResponse[ManagedFileArgs, ManagedFileState]{}, err
	}
	state := req.State
	if req.Inputs.ReloadSystemd != nil {
		reload := *req.Inputs.ReloadSystemd
		state.ReloadSystemd = &reload
	}
	state.Path, state.Mode, state.Owner, state.Group = req.ID, actual.Mode, actual.Owner, actual.Group
	state.Digest = digestOf([]byte(actual.Content))
	inputs := req.Inputs
	if inputs.Path == "" {
		// The content is left out: an import cannot know whether the program marks it secret, and
		// the engine checkpoints these inputs as they are. The digest in the state is what Diff uses.
		inputs = ManagedFileArgs{Path: req.ID, Mode: &state.Mode, Owner: &state.Owner,
			Group: &state.Group, ReloadSystemd: state.ReloadSystemd}
	}
	return infer.ReadResponse[ManagedFileArgs, ManagedFileState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update rewrites the file, unless the machine already has it exactly as wanted. Rewriting
// identical content is not harmless: it moves the mtime, and with reloadSystemd reloads the daemon
// for no reason.
func (ManagedFile) Update(ctx context.Context, req infer.UpdateRequest[ManagedFileArgs, ManagedFileState]) (infer.UpdateResponse[ManagedFileState], error) {
	w := wantedOf(req.Inputs)
	w.path = req.ID
	if req.DryRun {
		return infer.UpdateResponse[ManagedFileState]{Output: w.state()}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[ManagedFileState]{}, err
	}
	current, err := readManagedFile(ctx, m, w.path)
	if err != nil {
		return infer.UpdateResponse[ManagedFileState]{}, err
	}
	same := current != nil && current.Content == w.content &&
		current.Mode == w.mode && current.Owner == w.owner && current.Group == w.group
	if !same {
		if err := writeManagedFile(ctx, m, w); err != nil {
			return infer.UpdateResponse[ManagedFileState]{}, err
		}
	}
	return infer.UpdateResponse[ManagedFileState]{Output: w.state()}, nil
}

// Diff is spelled out so the reason for a replacement is legible: a file at a new path is a new
// file (created before the old one is removed), while editing one in place is not.
func (ManagedFile) Diff(_ context.Context, req infer.DiffRequest[ManagedFileArgs, ManagedFileState]) (p.DiffResponse, error) {
	w := wantedOf(req.Inputs)
	old := req.State
	diff := map[string]p.PropertyDiff{}
	if old.Digest != digestOf([]byte(w.content)) {
		diff["content"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Mode != w.mode {
		diff["mode"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Owner != w.owner {
		diff["owner"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Group != w.group {
		diff["group"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.ReloadSystemd != nil && *old.ReloadSystemd != w.reloadSystemd {
		diff["reloadSystemd"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Path != w.path {
		diff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	return p.DiffResponse{HasChanges: len(diff) > 0, DetailedDiff: diff, DeleteBeforeReplace: false}, nil
}

// Delete removes the file: the file is the whole of this resource, and nothing else at or under
// its path is touched.
func (ManagedFile) Delete(ctx context.Context, req infer.DeleteRequest[ManagedFileState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate("rm -f "+shell.Quote(req.ID)))
	return infer.DeleteResponse{}, err
}
