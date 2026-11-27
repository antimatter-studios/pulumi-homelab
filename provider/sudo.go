package homelab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
)

// SudoRule is a sudo privilege, as a file in /etc/sudoers.d. Ported from src/resources/sudo.ts.
//
// The content is validated with visudo -c in a temporary file and installed only if it parses; the
// name is checked because sudo silently ignores a drop-in with a dot or a tilde in it; the mode is
// 0440 owned by root, set and read back.
type SudoRule struct{}

// SudoRuleArgs are the inputs of a SudoRule.
type SudoRuleArgs struct {
	User         *string  `pulumi:"user,optional"`
	Group        *string  `pulumi:"group,optional"`
	Passwordless bool     `pulumi:"passwordless"`
	Commands     []string `pulumi:"commands,optional"`
	RunAs        *string  `pulumi:"runAs,optional"`
	File         *string  `pulumi:"file,optional"`
	Directory    *string  `pulumi:"directory,optional"`
}

// SudoRuleState is what is kept in state.
type SudoRuleState struct {
	File      string   `pulumi:"file"`
	Directory string   `pulumi:"directory"`
	Content   string   `pulumi:"content"`
	Mode      string   `pulumi:"mode"`
	Owner     string   `pulumi:"owner"`
	Commands  []string `pulumi:"commands,optional"`
}

func (a *SudoRuleArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.User, "The account this is about. Exactly one of user or group.")
	an.Describe(&a.Group, "A group instead, written %wheel in sudoers and given here without the %.")
	an.Describe(&a.Passwordless, "Whether the commands run without a password. Required rather than defaulted.")
	an.Describe(&a.Commands, "What may be run, as sudoers spells it. Full paths, or ['ALL'] (the default).")
	an.Describe(&a.RunAs, "Who the commands may be run as. ALL by default.")
	an.Describe(&a.File, "The file's name in the drop-in directory. Defaults to the resource's own name.")
	an.Describe(&a.Directory, "Which drop-in directory, for a machine that does not keep it at /etc/sudoers.d.")
}

func (r *SudoRule) Annotate(a infer.Annotator) {
	a.Describe(&r, "A sudo privilege the machine should grant, checked against the file rather than remembered.")
	a.SetToken("index", "SudoRule")
}

const (
	sudoersDirectory = "/etc/sudoers.d"
	sudoersMode      = "0440"
)

func sudoersPath(file, directory string) string { return directory + "/" + file }

// sudoersFileName checks a drop-in name, because sudo will not.
func sudoersFileName(name string) (string, error) {
	if name == "" {
		return "", errors.New("a sudoers drop-in needs a name")
	}
	if strings.Contains(name, ".") || strings.Contains(name, "~") {
		return "", fmt.Errorf("sudo ignores files in %s whose name contains '.' or '~', so '%s' would never apply", sudoersDirectory, name)
	}
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("'%s' is a path, and a sudoers drop-in is a single file name", name)
	}
	return name, nil
}

// sudoersLine is the rule itself: who, on which hosts, as whom, and what.
func sudoersLine(a SudoRuleArgs) (string, error) {
	if (a.User == nil) == (a.Group == nil) {
		return "", errors.New("a sudo rule names exactly one of a user or a group")
	}
	who := ""
	if a.User != nil {
		who = *a.User
	} else {
		who = "%" + *a.Group
	}
	commands := []string{"ALL"}
	if a.Commands != nil {
		commands = a.Commands
	}
	tag := ""
	if a.Passwordless {
		tag = "NOPASSWD: "
	}
	runAs := "ALL"
	if a.RunAs != nil {
		runAs = *a.RunAs
	}
	return fmt.Sprintf("%s ALL=(%s) %s%s", who, runAs, tag, strings.Join(commands, ", ")), nil
}

// sudoersFile is the whole file, with the note that explains itself to whoever finds it.
func sudoersFile(a SudoRuleArgs) (string, error) {
	line, err := sudoersLine(a)
	if err != nil {
		return "", err
	}
	return "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n" + line + "\n", nil
}

var sudoersRule = regexp.MustCompile(`^(\S+) ALL=\(([^)]*)\) (NOPASSWD: )?(.*)$`)

// parseSudoersFile recovers the inputs from a file sudoersFile wrote, and refuses any other file.
func parseSudoersFile(content string) (SudoRuleArgs, error) {
	lines := strings.Split(content, "\n")
	if len(lines) != 3 || lines[2] != "" {
		return SudoRuleArgs{}, errors.New("it is not a single rule written by this resource")
	}
	found := sudoersRule.FindStringSubmatch(lines[1])
	if found == nil {
		return SudoRuleArgs{}, errors.New("its rule is not one this resource writes")
	}
	args := SudoRuleArgs{Passwordless: found[3] != ""}
	if group, ok := strings.CutPrefix(found[1], "%"); ok {
		args.Group = &group
	} else {
		user := found[1]
		args.User = &user
	}
	runAs := found[2]
	args.RunAs = &runAs
	args.Commands = strings.Split(found[4], ", ")
	written, err := sudoersFile(args)
	if err != nil || written != content {
		return SudoRuleArgs{}, errors.New("it is not exactly the file this resource would write")
	}
	return args, nil
}

func readSudoRule(ctx context.Context, m *machine, file, directory string) (*SudoRuleState, error) {
	path := sudoersPath(file, directory)
	asked, err := m.ask(ctx, m.escalate("test -f "+shellQuote(path)+" || exit 9; "+
		"stat -c '%a %U' "+shellQuote(path)+" && cat "+shellQuote(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	head, content := firstLineSplit(asked.out)
	words := append(jscompat.Split(jscompat.Trim(head)), "", "")
	return &SudoRuleState{File: file, Directory: directory, Content: content, Mode: mode.NormaliseMode(words[0]), Owner: words[1]}, nil
}

// sudoApplyCommand writes the file only once visudo agrees it is a sudoers file.
func sudoApplyCommand(file, content, directory string) string {
	path := sudoersPath(file, directory)
	return "umask 077 && staging=$(mktemp) && " +
		heredocInto(`"$staging"`, content) + "\n" +
		`visudo -cqf "$staging" || { rm -f "$staging"; echo "sudoers content did not parse" >&2; exit 1; }; ` +
		"install -m " + sudoersMode + ` -o root -g root "$staging" ` + shellQuote(path) + ` && rm -f "$staging"`
}

func sudoDirectory(a SudoRuleArgs) string {
	if a.Directory != nil {
		return *a.Directory
	}
	return sudoersDirectory
}

// Check defaults the file to the resource's name and refuses a name sudo would ignore, as the
// TypeScript constructor did, so a bad rule fails at preview.
func (SudoRule) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SudoRuleArgs], error) {
	args, failures, err := infer.DefaultCheck[SudoRuleArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[SudoRuleArgs]{Inputs: args, Failures: failures}, err
	}
	file := req.Name
	if args.File != nil {
		file = *args.File
	}
	if _, err := sudoersFileName(file); err != nil {
		return infer.CheckResponse[SudoRuleArgs]{Inputs: args, Failures: []p.CheckFailure{{Property: "file", Reason: err.Error()}}}, nil
	}
	args.File = &file
	if args.Directory == nil {
		d := sudoersDirectory
		args.Directory = &d
	}
	return infer.CheckResponse[SudoRuleArgs]{Inputs: args}, nil
}

// Create validates and installs the file.
func (SudoRule) Create(ctx context.Context, req infer.CreateRequest[SudoRuleArgs]) (infer.CreateResponse[SudoRuleState], error) {
	file, err := sudoersFileName(deref(req.Inputs.File))
	if err != nil {
		return infer.CreateResponse[SudoRuleState]{}, err
	}
	directory := sudoDirectory(req.Inputs)
	content, err := sudoersFile(req.Inputs)
	if err != nil {
		return infer.CreateResponse[SudoRuleState]{}, err
	}
	state := SudoRuleState{File: file, Directory: directory, Content: content, Mode: sudoersMode, Owner: "root", Commands: req.Inputs.Commands}
	if req.DryRun {
		return infer.CreateResponse[SudoRuleState]{ID: file, Output: state}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SudoRuleState]{}, err
	}
	if _, err := m.must(ctx, m.escalate(sudoApplyCommand(file, content, directory))); err != nil {
		return infer.CreateResponse[SudoRuleState]{}, err
	}
	return infer.CreateResponse[SudoRuleState]{ID: file, Output: state}, nil
}

// Read reports the file; gone from the machine, it is forgotten and the next up puts it back.
func (SudoRule) Read(ctx context.Context, req infer.ReadRequest[SudoRuleArgs, SudoRuleState]) (infer.ReadResponse[SudoRuleArgs, SudoRuleState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SudoRuleArgs, SudoRuleState]{}, err
	}
	// an import remembers nothing and reads the default directory; a refresh uses the directory it
	// wrote to, even one declared empty
	imported := req.State.File == ""
	directory := req.State.Directory
	if imported {
		directory = sudoersDirectory
	}
	actual, err := readSudoRule(ctx, m, req.ID, directory)
	if err != nil || actual == nil {
		return infer.ReadResponse[SudoRuleArgs, SudoRuleState]{}, err
	}
	inputs := req.Inputs
	if imported {
		// Use the reconstructed rule when the file matches the resource's format.
		parsed, _ := parseSudoersFile(actual.Content)
		parsed.File, parsed.Directory = &actual.File, &actual.Directory
		inputs = parsed
	}
	actual.Commands = inputs.Commands
	return infer.ReadResponse[SudoRuleArgs, SudoRuleState]{ID: req.ID, Inputs: inputs, State: *actual}, nil
}

// Update rewrites the file only when its content or mode differ.
func (SudoRule) Update(ctx context.Context, req infer.UpdateRequest[SudoRuleArgs, SudoRuleState]) (infer.UpdateResponse[SudoRuleState], error) {
	content, err := sudoersFile(req.Inputs)
	if err != nil {
		return infer.UpdateResponse[SudoRuleState]{}, err
	}
	directory := sudoDirectory(req.Inputs)
	state := SudoRuleState{File: req.ID, Directory: directory, Content: content, Mode: sudoersMode, Owner: "root", Commands: req.Inputs.Commands}
	if req.DryRun {
		return infer.UpdateResponse[SudoRuleState]{Output: state}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SudoRuleState]{}, err
	}
	current, err := readSudoRule(ctx, m, req.ID, directory)
	if err != nil {
		return infer.UpdateResponse[SudoRuleState]{}, err
	}
	if current == nil || current.Content != content || current.Mode != sudoersMode {
		if _, err := m.must(ctx, m.escalate(sudoApplyCommand(req.ID, content, directory))); err != nil {
			return infer.UpdateResponse[SudoRuleState]{}, err
		}
	}
	return infer.UpdateResponse[SudoRuleState]{Output: state}, nil
}

// Diff: a rule in a different file is a different rule (delete before replace).
func (SudoRule) Diff(_ context.Context, req infer.DiffRequest[SudoRuleArgs, SudoRuleState]) (p.DiffResponse, error) {
	old := req.State
	content, err := sudoersFile(req.Inputs)
	if err != nil {
		return p.DiffResponse{}, err
	}
	file := old.File
	if req.Inputs.File != nil {
		file = *req.Inputs.File
	}
	var replaces []string
	if old.File != file {
		replaces = append(replaces, "file")
	}
	return diffOf(old.Content != content || old.Mode != sudoersMode || old.Owner != "root", true, replaces...), nil
}

// Delete removes the file from the directory in state, so a rule declared into another directory
// is not left granting privilege while the wrong path is removed.
func (SudoRule) Delete(ctx context.Context, req infer.DeleteRequest[SudoRuleState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	// the directory in state as written, even an explicitly empty one: substituting the default
	// would remove a different drop-in of the same name (review of #98)
	directory := req.State.Directory
	_, err = m.must(ctx, m.escalate("rm -f "+shellQuote(sudoersPath(req.ID, directory))))
	return infer.DeleteResponse{}, err
}
