package homelab

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/fstab"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// FstabEntry is one line of /etc/fstab, marked as this provider's and edited without disturbing
// any other byte. Ported from src/resources/fstab.ts. Delete takes the line out and never
// unmounts anything or touches what is on the filesystem.
type FstabEntry struct{}

// FstabEntryArgs are the inputs of an FstabEntry.
type FstabEntryArgs struct {
	Source  string   `pulumi:"source"`
	Target  string   `pulumi:"target"`
	Type    string   `pulumi:"type"`
	Options []string `pulumi:"options,optional"`
	Dump    *int     `pulumi:"dump,optional"`
	Pass    *int     `pulumi:"pass,optional"`
	Mount   *bool    `pulumi:"mount,optional"`
	File    *string  `pulumi:"file,optional"`
}

// FstabEntryState is what is kept in state.
type FstabEntryState struct {
	Source  string   `pulumi:"source"`
	Target  string   `pulumi:"target"`
	Type    string   `pulumi:"type"`
	Options []string `pulumi:"options"`
	Dump    int      `pulumi:"dump"`
	Pass    int      `pulumi:"pass"`
	Mount   bool     `pulumi:"mount"`
	Line    string   `pulumi:"line"`
	Mounted *string  `pulumi:"mounted,optional"`
	File    string   `pulumi:"file"`
}

func (a *FstabEntryArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Source, "What is mounted: a device, UUID=…, LABEL=… or a remote share.")
	an.Describe(&a.Target, "The mount point, which identifies the entry.")
	an.Describe(&a.Type, "The filesystem type.")
	an.Describe(&a.Options, "Mount options; ['defaults'] by default.")
	an.Describe(&a.Dump, "The dump field, 0 by default.")
	an.Describe(&a.Pass, "The fsck pass, 0 by default.")
	an.Describe(&a.Mount, "Whether to mount it now if it is not mounted. Off by default.")
	an.Describe(&a.File, "The fstab, /etc/fstab by default.")
}

func (s *FstabEntryState) Annotate(an infer.Annotator) {
	an.Describe(&s.Line, "The entry as the file has it.")
	an.Describe(&s.Mounted, "What findmnt says is mounted there, if anything.")
}

func (r *FstabEntry) Annotate(a infer.Annotator) {
	a.Describe(&r, "One fstab entry, edited in place without disturbing the rest of the file.")
	a.SetToken("index", "FstabEntry")
}

const fstabPath = "/etc/fstab"

func fstabWanted(a FstabEntryArgs) FstabEntryState {
	s := FstabEntryState{Source: a.Source, Target: a.Target, Type: a.Type, Options: a.Options, Dump: deref(a.Dump), Pass: deref(a.Pass), Mount: deref(a.Mount), File: orDefault(a.File, fstabPath)}
	if s.Options == nil {
		s.Options = []string{"defaults"}
	}
	return s
}

func fstabLineOf(s FstabEntryState) string {
	return fstab.Line(fstab.Entry{Source: s.Source, Target: s.Target, Type: s.Type, Options: s.Options, Dump: s.Dump, Pass: s.Pass})
}

// normalFstabLine is the line with its whitespace collapsed, which is all fstab itself reads; so
// tabs in a hand-written line are not drift (an import of one previews as no change).
type fstabRead struct {
	line    *string
	mounted *string
}

func readFstabEntry(ctx context.Context, m *machine, target, file string) (fstabRead, error) {
	contents, err := m.commandResult(ctx, "cat fstab", m.escalate(whenFilePresent(file, "cat -- "+shellQuote(file))))
	if err != nil {
		return fstabRead{}, err
	}
	line, ok := fstab.FindEntry(contents.out, target)
	if !ok {
		return fstabRead{}, nil
	}

	// With a filter, findmnt uses exit 1 for both no match and failures.
	// Read the mount table successfully first, then look for the target.
	mounts, err := m.commandResult(ctx, "findmnt", m.escalate("findmnt --kernel --list --json -o TARGET,SOURCE,FSTYPE,OPTIONS"))
	if err != nil {
		return fstabRead{}, err
	}
	var table struct {
		Filesystems []struct {
			Target, Source, Fstype, Options string
		}
	}
	if err := json.Unmarshal([]byte(mounts.out), &table); err != nil || table.Filesystems == nil {
		return fstabRead{}, errors.New("findmnt returned an invalid mount table")
	}
	var matches []string
	for _, mount := range table.Filesystems {
		if mount.Target == target {
			matches = append(matches, strings.Join([]string{mount.Source, mount.Fstype, mount.Options}, " "))
		}
	}
	read := fstabRead{line: &line}
	if len(matches) != 0 {
		mounted := strings.Join(matches, "\n")
		read.mounted = &mounted
	}
	return read, nil
}

func settleFstab(ctx context.Context, m *machine, wanted FstabEntryState) (FstabEntryState, error) {
	line := fstabLineOf(wanted)
	current, err := m.must(ctx, m.escalate("cat "+shellQuote(wanted.File)))
	if err != nil {
		return wanted, err
	}
	if updated := fstab.Upsert(current, wanted.Target, line); updated != current {
		if _, err := m.must(ctx, m.escalate(heredoc(wanted.File, updated))); err != nil {
			return wanted, err
		}
	}
	if wanted.Mount {
		if _, err := m.must(ctx, m.escalate("mountpoint -q "+shellQuote(wanted.Target)+" || mount "+shellQuote(wanted.Target))); err != nil {
			return wanted, err
		}
	}
	actual, err := readFstabEntry(ctx, m, wanted.Target, wanted.File)
	if err != nil {
		return wanted, err
	}
	wanted.Line, wanted.Mounted = line, actual.mounted
	if actual.line != nil {
		wanted.Line = *actual.line
	}
	return wanted, nil
}

// entryFromLine reads the six fields of an entry back into inputs, for an import.
func entryFromLine(line string) (FstabEntryState, bool) {
	f := jscompat.Fields(line)
	if len(f) != 6 {
		return FstabEntryState{}, false
	}
	dump, err1 := strconv.Atoi(f[4])
	pass, err2 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil {
		return FstabEntryState{}, false
	}
	return FstabEntryState{Source: f[0], Target: f[1], Type: f[2], Options: strings.Split(f[3], ","), Dump: dump, Pass: pass}, true
}

// Create writes the entry; the id is the mount point.
func (FstabEntry) Create(ctx context.Context, req infer.CreateRequest[FstabEntryArgs]) (infer.CreateResponse[FstabEntryState], error) {
	wanted := fstabWanted(req.Inputs)
	if req.DryRun {
		wanted.Line = fstabLineOf(wanted)
		return infer.CreateResponse[FstabEntryState]{ID: wanted.Target, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[FstabEntryState]{}, err
	}
	state, err := settleFstab(ctx, m, wanted)
	if err != nil {
		return infer.CreateResponse[FstabEntryState]{}, err
	}
	return infer.CreateResponse[FstabEntryState]{ID: wanted.Target, Output: state}, nil
}

// Read reports the entry; gone from the file, it is forgotten.
func (FstabEntry) Read(ctx context.Context, req infer.ReadRequest[FstabEntryArgs, FstabEntryState]) (infer.ReadResponse[FstabEntryArgs, FstabEntryState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[FstabEntryArgs, FstabEntryState]{}, err
	}
	state := req.State
	imported := state.Target == "" && state.File == ""
	if imported {
		state.File = fstabPath
	}
	actual, err := readFstabEntry(ctx, m, req.ID, state.File)
	if err != nil || actual.line == nil {
		return infer.ReadResponse[FstabEntryArgs, FstabEntryState]{}, err
	}
	inputs := req.Inputs
	if imported {
		parsed, ok := entryFromLine(*actual.line)
		if !ok {
			parsed = fstabWanted(FstabEntryArgs{Target: req.ID})
		}
		parsed.File = state.File
		state = parsed
		inputs = FstabEntryArgs{Source: parsed.Source, Target: parsed.Target, Type: parsed.Type, Options: parsed.Options, Dump: &parsed.Dump, Pass: &parsed.Pass, Mount: &parsed.Mount, File: &parsed.File}
	}
	if state.Options == nil {
		state.Options = []string{}
	}
	state.Target, state.Line, state.Mounted = req.ID, *actual.line, actual.mounted
	return infer.ReadResponse[FstabEntryArgs, FstabEntryState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update writes the entry again under its mount point.
func (FstabEntry) Update(ctx context.Context, req infer.UpdateRequest[FstabEntryArgs, FstabEntryState]) (infer.UpdateResponse[FstabEntryState], error) {
	wanted := fstabWanted(req.Inputs)
	wanted.Target = req.ID
	if req.DryRun {
		wanted.Line, wanted.Mounted = fstabLineOf(wanted), req.State.Mounted
		return infer.UpdateResponse[FstabEntryState]{Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[FstabEntryState]{}, err
	}
	state, err := settleFstab(ctx, m, wanted)
	return infer.UpdateResponse[FstabEntryState]{Output: state}, err
}

// Diff compares the observed entry with the declared line. A different mount point replaces.
func (FstabEntry) Diff(_ context.Context, req infer.DiffRequest[FstabEntryArgs, FstabEntryState]) (p.DiffResponse, error) {
	old, wanted := req.State, fstabWanted(req.Inputs)
	var replaces []string
	if old.Target != wanted.Target {
		replaces = []string{"target"}
	}
	return diffOf(old.Line != fstabLineOf(wanted), true, replaces...), nil
}

// Delete takes out the fstab entry without unmounting it.
func (FstabEntry) Delete(ctx context.Context, req infer.DeleteRequest[FstabEntryState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	file := req.State.File
	current, err := m.must(ctx, m.escalate("cat "+shellQuote(file)))
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(heredoc(file, fstab.Remove(current, req.ID))))
	return infer.DeleteResponse{}, err
}
