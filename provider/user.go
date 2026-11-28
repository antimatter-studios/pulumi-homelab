package homelab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/getent"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// User is a system account for something that runs. Ported from src/resources/user.ts.
//
// groups is the whole list, not additions to it, and memberships the machine has that it does not
// mention are refused rather than removed unless allowGroupRemoval is set. Deleting one leaves the
// home directory behind: it is where a service account's data lives.
type User struct{}

// UserArgs are the inputs of a User.
type UserArgs struct {
	Name              string   `pulumi:"name"`
	Shell             string   `pulumi:"shell"`
	Home              *string  `pulumi:"home,optional"`
	CreateHome        *bool    `pulumi:"createHome,optional"`
	Groups            []string `pulumi:"groups,optional"`
	Uid               *int     `pulumi:"uid,optional"`
	Group             *string  `pulumi:"group,optional"`
	AllowGroupRemoval *bool    `pulumi:"allowGroupRemoval,optional"`
}

// UserState is what is kept in state.
type UserState struct {
	Name              string   `pulumi:"name"`
	Uid               *int     `pulumi:"uid,optional"`
	Group             *string  `pulumi:"group,optional"`
	Shell             string   `pulumi:"shell"`
	Home              string   `pulumi:"home"`
	CreateHome        bool     `pulumi:"createHome"`
	Groups            []string `pulumi:"groups"`
	AllowGroupRemoval bool     `pulumi:"allowGroupRemoval"`
}

func (a *UserArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The account's name.")
	an.Describe(&a.Shell, "The login shell. Required, with no default: /usr/sbin/nologin for something that only runs, the person's real shell otherwise.")
	an.Describe(&a.Home, "Where its home is.")
	an.Describe(&a.CreateHome, "Whether a home directory is made. On by default.")
	an.Describe(&a.Groups, "Groups it belongs to besides its own. This is the whole list, not additions to it.")
	an.Describe(&a.Uid, "The numeric uid, set when the account is created. An existing account with a different uid is refused rather than renumbered.")
	an.Describe(&a.Group, "The primary group's name, which must already exist. Left out, useradd makes a group named after the account.")
	an.Describe(&a.AllowGroupRemoval, "Permission to remove memberships the arguments do not mention. Off by default.")
}

func (r *User) Annotate(a infer.Annotator) {
	a.Describe(&r, "An account the machine should have, checked against passwd rather than remembered.")
	a.SetToken("index", "User")
}

// noLoginShells are the shells that mean "this account cannot log in".
var noLoginShells = []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"}

// groupsToLose is the memberships the machine has that the declaration does not mention, sorted.
func groupsToLose(actual, declared []string) []string {
	losing := []string{}
	for _, g := range actual {
		if !slices.Contains(declared, g) {
			losing = append(losing, g)
		}
	}
	slices.SortFunc(losing, jscompat.CompareUTF16)
	return losing
}

// uidRefusal says why an existing account cannot be made to match a declared uid. actual is the
// uid getent printed, already formatted: "NaN" for a field Number() could not read, which is what
// the TypeScript reported for it (#45, the uid NaN note).
func uidRefusal(name string, declared *int, actual *string) string {
	if declared == nil || actual == nil || fmt.Sprint(*declared) == *actual {
		return ""
	}
	return fmt.Sprintf("%s exists with uid %s, and the code says %d. Renumbering an account leaves ", name, *actual, *declared) +
		"every file it owns outside its home owned by the old number, so this is refused. Change the files and " +
		fmt.Sprintf("the account by hand (usermod -u, then chown everything the old uid owned), or declare uid %s.", *actual)
}

// uidText is the uid as the TypeScript printed it: the number, or NaN for one it could not read.
func uidText(read *getent.UserRead) *string {
	if read == nil {
		return nil
	}
	text := "NaN"
	if read.UID != nil {
		text = fmt.Sprint(*read.UID)
	}
	return &text
}

type lockoutArgs struct {
	Name              string
	Shell             string
	Groups            []string
	AllowGroupRemoval bool
}

// lockoutRefusal says why a change to an account is refused: giving the connecting account a shell
// it cannot log in with, or dropping memberships nobody said to drop. connectsAs is nil for a
// transport that is not an ssh login; actualGroups is nil for an account that does not exist yet.
func lockoutRefusal(args lockoutArgs, connectsAs *string, actualGroups []string, exists bool) string {
	if connectsAs != nil && args.Name == *connectsAs && slices.Contains(noLoginShells, args.Shell) {
		return fmt.Sprintf("%s is the account this provider connects as, and the code gives it %s. ", args.Name, args.Shell) +
			"That would take away the login being used to apply it. Give it a real shell, or manage a different account."
	}
	losing := []string{}
	if exists {
		losing = groupsToLose(actualGroups, args.Groups)
	}
	if len(losing) > 0 && !args.AllowGroupRemoval {
		plural, sudo := "s", ""
		if len(losing) == 1 {
			plural = ""
		}
		if slices.Contains(losing, "sudo") {
			sudo = ", including sudo"
		}
		return fmt.Sprintf("%s is in %s, which the code does not mention. ", args.Name, strings.Join(losing, ", ")) +
			fmt.Sprintf("`groups` is the whole list rather than additions to it, so applying this would remove %d ", len(losing)) +
			fmt.Sprintf("membership%s%s. ", plural, sudo) +
			"Add them to the list, or set allowGroupRemoval: true if losing them is what you meant."
	}
	return ""
}

// readUser is what the machine says about the account, or nil when there is no such user.
func readUser(ctx context.Context, m *machine, name string) (*getent.UserRead, error) {
	q := shellQuote(name)
	asked, err := m.ask(ctx, "getent passwd "+q+" && id -nG "+q+" && id -gn "+q)
	if err != nil {
		return nil, err
	}
	if asked.code != 0 {
		return nil, nil
	}
	read := getent.ParseUserRead(name, asked.out)
	return &read, nil
}

// userCommand is the one command that makes the account: usermod when it exists, useradd when not.
// The spacing, empty parts included, is the TypeScript's.
func userCommand(s UserState) string {
	name := shellQuote(s.Name)
	groups, home, uid, primary := "", "", "", ""
	if len(s.Groups) > 0 {
		groups = "-G " + shellQuote(strings.Join(s.Groups, ","))
	}
	if s.Home != "" {
		home = "-d " + shellQuote(s.Home)
	}
	if s.Uid != nil {
		uid = fmt.Sprintf("-u %d", *s.Uid)
	}
	if s.Group != nil && *s.Group != "" {
		primary = "-g " + shellQuote(*s.Group)
	}
	makeHome := "-M"
	if s.CreateHome {
		makeHome = "-m"
	}
	return "if getent passwd " + name + " >/dev/null; then " +
		"usermod -s " + shellQuote(s.Shell) + " " + home + " " + primary + " " + groups + " " + name + "; " +
		"else " +
		"useradd --system " + makeHome + " -s " + shellQuote(s.Shell) + " " + uid + " " + primary + " " + home + " " + groups + " " + name + "; " +
		"fi"
}

func settleUser(ctx context.Context, m *machine, s UserState) error {
	connectsAs := m.cfg.User
	if refusal := lockoutRefusal(lockoutArgs{Name: s.Name, Shell: s.Shell}, &connectsAs, nil, false); refusal != "" {
		return errors.New(refusal)
	}
	actual, err := readUser(ctx, m, s.Name)
	if err != nil {
		return err
	}
	if refusal := uidRefusal(s.Name, s.Uid, uidText(actual)); refusal != "" {
		return errors.New(refusal)
	}
	var actualGroups []string
	if actual != nil {
		actualGroups = actual.Groups
	}
	if refusal := lockoutRefusal(lockoutArgs{Name: s.Name, Groups: s.Groups, AllowGroupRemoval: s.AllowGroupRemoval}, nil, actualGroups, actual != nil); refusal != "" {
		return errors.New(refusal)
	}
	_, err = m.must(ctx, m.escalate(userCommand(s)))
	return err
}

// withActual is `{ ...wanted, ...actual }`.
func withActual(s UserState, actual *getent.UserRead) UserState {
	if actual == nil {
		return s
	}
	group := actual.Group
	s.Name, s.Uid, s.Group, s.Home, s.Shell, s.Groups = actual.Name, actual.UID, &group, actual.Home, actual.Shell, actual.Groups
	return s
}

func userWanted(a UserArgs) UserState {
	s := UserState{
		Name: a.Name, Uid: a.Uid, Group: a.Group, Shell: a.Shell, Home: "/home/" + a.Name,
		CreateHome: true, Groups: []string{}, AllowGroupRemoval: deref(a.AllowGroupRemoval),
	}
	if a.Home != nil {
		s.Home = *a.Home
	}
	if a.CreateHome != nil {
		s.CreateHome = *a.CreateHome
	}
	if a.Groups != nil {
		s.Groups = a.Groups
	}
	return s
}

// Create makes the account, or adopts one that is there, after the refusals.
func (User) Create(ctx context.Context, req infer.CreateRequest[UserArgs]) (infer.CreateResponse[UserState], error) {
	wanted := userWanted(req.Inputs)
	if req.DryRun {
		return infer.CreateResponse[UserState]{ID: req.Inputs.Name, Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	if err := settleUser(ctx, m, wanted); err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	actual, err := readUser(ctx, m, req.Inputs.Name)
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	return infer.CreateResponse[UserState]{ID: req.Inputs.Name, Output: withActual(wanted, actual)}, nil
}

// Read reports what passwd says; an account gone from the machine is forgotten.
func (User) Read(ctx context.Context, req infer.ReadRequest[UserArgs, UserState]) (infer.ReadResponse[UserArgs, UserState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[UserArgs, UserState]{}, err
	}
	actual, err := readUser(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[UserArgs, UserState]{}, err
	}
	if actual == nil {
		return infer.ReadResponse[UserArgs, UserState]{}, nil
	}
	state := req.State
	if state.Name == "" {
		// imported: nothing remembered, so the defaults the TypeScript read falls back to
		state.CreateHome = true
	}
	state = withActual(state, actual)
	inputs := req.Inputs
	if inputs.Name == "" {
		inputs = UserArgs{Name: state.Name, Uid: state.Uid, Shell: state.Shell, Home: &state.Home, Groups: state.Groups, Group: state.Group}
	}
	return infer.ReadResponse[UserArgs, UserState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the account under its id: `{ ...old, ...args, name: id }`.
func (User) Update(ctx context.Context, req infer.UpdateRequest[UserArgs, UserState]) (infer.UpdateResponse[UserState], error) {
	a := req.Inputs
	wanted := req.State
	wanted.Name, wanted.Shell = req.ID, a.Shell
	if a.Home != nil {
		wanted.Home = *a.Home
	}
	if a.CreateHome != nil {
		wanted.CreateHome = *a.CreateHome
	}
	wanted.Groups = a.Groups
	if wanted.Groups == nil {
		wanted.Groups = []string{}
	}
	if a.Uid != nil {
		wanted.Uid = a.Uid
	}
	if a.Group != nil {
		wanted.Group = a.Group
	}
	wanted.AllowGroupRemoval = deref(a.AllowGroupRemoval)
	if req.DryRun {
		return infer.UpdateResponse[UserState]{Output: wanted}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	if err := settleUser(ctx, m, wanted); err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	actual, err := readUser(ctx, m, req.ID)
	if err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	return infer.UpdateResponse[UserState]{Output: withActual(wanted, actual)}, nil
}

// Diff: a rename replaces (delete before replace); shell, home, uid, primary group and the sorted
// groups list are compared as the TypeScript compares them.
func (User) Diff(_ context.Context, req infer.DiffRequest[UserArgs, UserState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	groups := slices.Clone(a.Groups)
	slices.SortFunc(groups, jscompat.CompareUTF16)
	changed := old.Shell != a.Shell ||
		(a.Home != nil && old.Home != *a.Home) ||
		(a.Uid != nil && (old.Uid == nil || *old.Uid != *a.Uid)) ||
		(a.Group != nil && (old.Group == nil || *old.Group != *a.Group)) ||
		joined(old.Groups) != joined(groups)
	var replaces []string
	if old.Name != a.Name {
		replaces = append(replaces, "name")
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete removes the account but never its home: no --remove.
func (User) Delete(ctx context.Context, req infer.DeleteRequest[UserState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	// Query local passwd records since userdel only manages local accounts.
	account, err := m.commandResult(ctx, "getent passwd", "getent -s files passwd -- "+shellQuote(req.ID), 2)
	if err != nil || account.code == 2 {
		return infer.DeleteResponse{}, err
	}
	actual := getent.ParseUserRead(req.ID, account.out)
	name, _, _ := strings.Cut(account.out, ":")
	if name != req.ID || req.State.Uid == nil || actual.UID == nil || *req.State.Uid != *actual.UID {
		warnRetained(ctx, req.ID, "its local UID cannot be verified against state")
		return infer.DeleteResponse{}, nil
	}
	// userdel exit 6 means the account disappeared after lookup. An inner
	// check avoids confusing this with the exit status of sudo itself.
	command := "userdel -- " + shellQuote(req.ID) + `; status=$?; [ "$status" -eq 6 ] || exit "$status"`
	_, err = m.commandResult(ctx, "userdel", m.escalate(command))
	return infer.DeleteResponse{}, err
}
