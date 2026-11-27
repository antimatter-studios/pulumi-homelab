package homelab

import (
	"context"
	"fmt"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/journald"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// Journald is a journald drop-in. Ported from src/resources/journald.ts. Only the drop-in is
// compared; what journald ends up with (effective) and which settings another file overrides are
// reported, not reconciled. Delete removes the drop-in and never the journal or its logs.
type Journald struct{}

// JournaldArgs are the inputs of a Journald.
type JournaldArgs struct {
	Settings  map[string]string `pulumi:"settings"`
	File      *string           `pulumi:"file,optional"`
	Directory *string           `pulumi:"directory,optional"`
	Journal   *string           `pulumi:"journal,optional"`
}

// JournaldState is what is kept in state.
type JournaldState struct {
	File       string            `pulumi:"file"`
	Content    string            `pulumi:"content"`
	Effective  map[string]string `pulumi:"effective"`
	Overridden []string          `pulumi:"overridden"`
	Persistent bool              `pulumi:"persistent"`
	JournalDir string            `pulumi:"journalDir"`
	Directory  string            `pulumi:"directory"`
	Journal    string            `pulumi:"journal"`
	Settings   map[string]string `pulumi:"settings,optional"`
}

func (a *JournaldArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Settings, "[Journal] settings, as journald.conf spells them. Written in key order.")
	an.Describe(&a.File, "The drop-in's name without .conf. Defaults to 90-<resource name>.")
	an.Describe(&a.Directory, "The drop-in directory, /etc/systemd/journald.conf.d by default.")
	an.Describe(&a.Journal, "The persistent journal directory reported on, /var/log/journal by default.")
}

func (s *JournaldState) Annotate(an infer.Annotator) {
	an.Describe(&s.Effective, "What journald ends up with, every file merged.")
	an.Describe(&s.Overridden, "Settings of this drop-in that another file overrides.")
	an.Describe(&s.Persistent, "Whether the journal directory exists, so logs survive a reboot.")
	an.Describe(&s.JournalDir, "What the journal path is: directory, absent, or a symlink's target.")
}

func (r *Journald) Annotate(a infer.Annotator) {
	a.Describe(&r, "A journald drop-in, with what journald actually ends up with reported beside it.")
	a.SetToken("index", "Journald")
}

const (
	journaldDirectory  = "/etc/systemd/journald.conf.d"
	journaldPersistent = "/var/log/journal"
)

func journaldPath(file, directory string) string { return directory + "/" + file + ".conf" }

// overriddenBy is the declared settings the effective configuration does not agree with, sorted.
func overriddenBy(settings, effective map[string]string) []string {
	out := []string{}
	for key, value := range settings {
		if got, ok := effective[key]; !ok || got != value {
			out = append(out, key)
		}
	}
	slices.SortFunc(out, jscompat.CompareUTF16)
	return out
}

func journaldReadCommand(path, journal string) string {
	return "test -f " + shellQuote(path) + " || exit 9; " +
		"cat " + shellQuote(path) + "; echo '#pulumi-homelab#effective'; " +
		"systemd-analyze cat-config systemd/journald.conf 2>/dev/null; " +
		"echo '#pulumi-homelab#persistent'; " +
		"if pointsAt=$(readlink " + shellQuote(journal) + " 2>/dev/null); then printf '%s' \"$pointsAt\"; " +
		"elif [ -d " + shellQuote(journal) + " ]; then echo directory; else echo absent; fi"
}

// splitOn is the TypeScript's splitOn: before and after the first `<marker>\n`.
func splitOn(text, marker string) (string, string) {
	at := strings.Index(text, marker+"\n")
	if at < 0 {
		return text, ""
	}
	return text[:at], text[at+len(marker)+1:]
}

func parseJournaldRead(out, file string, settings map[string]string, directory, journal string) JournaldState {
	ours, rest := splitOn(out, "#pulumi-homelab#effective")
	merged, reported := splitOn(rest, "#pulumi-homelab#persistent")
	effective := journald.ParseEffective(merged)
	journalDir := jscompat.Trim(reported)
	return JournaldState{
		File: file, Content: ours, Effective: effective, Overridden: overriddenBy(settings, effective),
		Persistent: journalDir != "absent", JournalDir: journalDir, Directory: directory, Journal: journal,
	}
}

func readJournald(ctx context.Context, m *machine, file string, settings map[string]string, directory, journal string) (*JournaldState, error) {
	asked, err := m.ask(ctx, m.escalate(journaldReadCommand(journaldPath(file, directory), journal)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read journald configuration: %s", jscompat.Trim(asked.err))
	}
	read := parseJournaldRead(asked.out, file, settings, directory, journal)
	return &read, nil
}

func journaldApplyCommand(file, content, directory string) string {
	path := journaldPath(file, directory)
	return "mkdir -p " + shellQuote(directory) + " && " + heredoc(path, content) + "\n" +
		"chmod 0644 " + shellQuote(path) + " && systemctl restart systemd-journald"
}

func settleJournald(ctx context.Context, m *machine, file string, a JournaldArgs) (JournaldState, error) {
	directory, journal := orDefault(a.Directory, journaldDirectory), orDefault(a.Journal, journaldPersistent)
	content := journald.JournaldFile(a.Settings)
	before, err := readJournald(ctx, m, file, a.Settings, directory, journal)
	if err != nil {
		return JournaldState{}, err
	}
	// an update that changes nothing restarts nothing
	if before == nil || before.Content != content {
		if _, err := m.must(ctx, m.escalate(journaldApplyCommand(file, content, directory))); err != nil {
			return JournaldState{}, err
		}
	}
	actual, err := readJournald(ctx, m, file, a.Settings, directory, journal)
	if err != nil {
		return JournaldState{}, err
	}
	if actual == nil {
		return JournaldState{}, fmt.Errorf("wrote %s but it is not there", journaldPath(file, directory))
	}
	actual.Settings = a.Settings
	return *actual, nil
}

// Check defaults the file to 90-<name> and the directories, as the TypeScript constructor did.
func (Journald) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[JournaldArgs], error) {
	args, failures, err := infer.DefaultCheck[JournaldArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[JournaldArgs]{Inputs: args, Failures: failures}, err
	}
	if args.File == nil {
		file := "90-" + req.Name
		args.File = &file
	}
	if args.Directory == nil {
		d := journaldDirectory
		args.Directory = &d
	}
	if args.Journal == nil {
		j := journaldPersistent
		args.Journal = &j
	}
	return infer.CheckResponse[JournaldArgs]{Inputs: args}, nil
}

// Create writes the drop-in and restarts journald when it differs; the id is the file.
func (Journald) Create(ctx context.Context, req infer.CreateRequest[JournaldArgs]) (infer.CreateResponse[JournaldState], error) {
	file := deref(req.Inputs.File)
	if req.DryRun {
		return infer.CreateResponse[JournaldState]{ID: file, Output: JournaldState{
			File: file, Content: journald.JournaldFile(req.Inputs.Settings), Settings: req.Inputs.Settings, Effective: map[string]string{}, Overridden: []string{},
			Directory: orDefault(req.Inputs.Directory, journaldDirectory), Journal: orDefault(req.Inputs.Journal, journaldPersistent),
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[JournaldState]{}, err
	}
	state, err := settleJournald(ctx, m, file, req.Inputs)
	if err != nil {
		return infer.CreateResponse[JournaldState]{}, err
	}
	return infer.CreateResponse[JournaldState]{ID: file, Output: state}, nil
}

// Read reports the drop-in and what journald ends up with; the drop-in gone, it is forgotten.
func (Journald) Read(ctx context.Context, req infer.ReadRequest[JournaldArgs, JournaldState]) (infer.ReadResponse[JournaldArgs, JournaldState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[JournaldArgs, JournaldState]{}, err
	}
	imported := req.State.File == ""
	directory, journal := req.State.Directory, req.State.Journal
	if imported {
		directory, journal = journaldDirectory, journaldPersistent
	}
	actual, err := readJournald(ctx, m, req.ID, journald.ParseEffective(req.State.Content), directory, journal)
	if err != nil || actual == nil {
		return infer.ReadResponse[JournaldArgs, JournaldState]{}, err
	}
	inputs := req.Inputs
	if imported {
		settings := journald.ParseEffective(actual.Content)
		actual.Overridden = overriddenBy(settings, actual.Effective)
		inputs = JournaldArgs{Settings: settings, File: &actual.File, Directory: &actual.Directory, Journal: &actual.Journal}
	}
	actual.Settings = inputs.Settings
	return infer.ReadResponse[JournaldArgs, JournaldState]{ID: req.ID, Inputs: inputs, State: *actual}, nil
}

// Update writes the drop-in again under its id.
func (Journald) Update(ctx context.Context, req infer.UpdateRequest[JournaldArgs, JournaldState]) (infer.UpdateResponse[JournaldState], error) {
	if req.DryRun {
		s := req.State
		s.Settings = req.Inputs.Settings
		s.Content = journald.JournaldFile(s.Settings)
		return infer.UpdateResponse[JournaldState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[JournaldState]{}, err
	}
	state, err := settleJournald(ctx, m, req.ID, req.Inputs)
	return infer.UpdateResponse[JournaldState]{Output: state}, err
}

// Diff compares only the drop-in; another file is a different drop-in (delete before replace).
func (Journald) Diff(_ context.Context, req infer.DiffRequest[JournaldArgs, JournaldState]) (p.DiffResponse, error) {
	old := req.State
	file := old.File
	if req.Inputs.File != nil {
		file = *req.Inputs.File
	}
	var replaces []string
	if old.File != file {
		replaces = []string{"file"}
	}
	return diffOf(old.Content != journald.JournaldFile(req.Inputs.Settings), true, replaces...), nil
}

// Delete removes the drop-in from the directory in state and restarts journald; the journal
// directory and its logs stay.
func (Journald) Delete(ctx context.Context, req infer.DeleteRequest[JournaldState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate("rm -f "+shellQuote(journaldPath(req.ID, req.State.Directory))+" && systemctl restart systemd-journald"))
	return infer.DeleteResponse{}, err
}
