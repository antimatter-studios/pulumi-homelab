package homelab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/acl"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// PosixAcl is named entries on a path's POSIX ACL, and only those. Ported from
// src/resources/acl.ts.
//
// It sets what it declares, removes what it declared before and no longer does, and never touches
// an entry somebody else put there (no setfacl -b or --set). The owner, group and other entries
// belong to Directory's mode and are refused as access entries; a mask is computed by setfacl and is
// refused in either scope. The optional normalise rule walks the tree with find on the machine,
// counting through wc, so nothing of the walk is buffered here.
type PosixAcl struct{}

// AclEntryArgs is one declared entry.
type AclEntryArgs struct {
	Type  string  `pulumi:"type"`
	Name  *string `pulumi:"name,optional"`
	Perms string  `pulumi:"perms"`
}

// AclEntrySettled is a declared entry settled: its name filled in and its permissions in rwx form.
type AclEntrySettled struct {
	Type  string `pulumi:"type"`
	Name  string `pulumi:"name"`
	Perms string `pulumi:"perms"`
}

// AclEntryActual is what getfacl reports for a declared entry: perms "" means absent.
type AclEntryActual struct {
	Type      string `pulumi:"type"`
	Name      string `pulumi:"name"`
	Perms     string `pulumi:"perms"`
	Effective string `pulumi:"effective"`
}

// AclNormaliseArgs is the rule that corrects directories under the path from one mode to another.
type AclNormaliseArgs struct {
	Group  string   `pulumi:"group"`
	From   string   `pulumi:"from"`
	To     string   `pulumi:"to"`
	Ignore []string `pulumi:"ignore,optional"`
}

// PosixAclArgs are the inputs of a PosixAcl.
type PosixAclArgs struct {
	Path           string            `pulumi:"path"`
	Entries        []AclEntryArgs    `pulumi:"entries,optional"`
	DefaultEntries []AclEntryArgs    `pulumi:"defaultEntries,optional"`
	Normalise      *AclNormaliseArgs `pulumi:"normalise,optional"`
}

// PosixAclState is what is kept in state.
type PosixAclState struct {
	Path           string            `pulumi:"path"`
	Entries        []AclEntrySettled `pulumi:"entries"`
	DefaultEntries []AclEntrySettled `pulumi:"defaultEntries"`
	Actual         []AclEntryActual  `pulumi:"actual"`
	DefaultActual  []AclEntryActual  `pulumi:"defaultActual"`
	Normalise      *AclNormaliseArgs `pulumi:"normalise,optional"`
	Normalised     *int              `pulumi:"normalised,optional"`
}

func (a *AclEntryArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Type, "user, group, mask or other. A mask is refused; user, group and other without a name are refused as access entries.")
	an.Describe(&a.Name, "Who the entry is for. Empty or absent for the owning entries, which only defaultEntries may set.")
	an.Describe(&a.Perms, "rwx, r-x, rw, an octal digit... settled to the three-letter form getfacl prints.")
}

func (a *AclNormaliseArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Group, "The group whose directories are corrected.")
	an.Describe(&a.From, "The exact mode to find, as in 2755.")
	an.Describe(&a.To, "The mode to set instead, as in 2775.")
	an.Describe(&a.Ignore, "Paths relative to the ACL's path that the walk never enters.")
}

func (a *PosixAclArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Path, "The path whose ACL this sets.")
	an.Describe(&a.Entries, "Access entries: named users and groups.")
	an.Describe(&a.DefaultEntries, "Default entries, which new entries under a directory inherit.")
	an.Describe(&a.Normalise, "Correct directories under the path that have one exact mode and group to another mode.")
}

func (a *PosixAclState) Annotate(an infer.Annotator) {
	an.Describe(&a.Actual, "What getfacl grants for each declared access entry, effective permissions included.")
	an.Describe(&a.DefaultActual, "What getfacl grants for each declared default entry.")
	an.Describe(&a.Normalised, "How many directories the last normalise changed.")
}

func (a *PosixAcl) Annotate(an infer.Annotator) {
	an.Describe(&a, "Named entries on a POSIX ACL, set and removed without touching anybody else's.")
	an.SetToken("index", "PosixAcl")
}

func (e AclEntryArgs) acl() acl.EntryArgs {
	return acl.EntryArgs{Type: acl.EntryType(e.Type), Name: deref(e.Name), Perms: e.Perms}
}

func (e AclEntrySettled) acl() acl.EntryArgs {
	return acl.EntryArgs{Type: acl.EntryType(e.Type), Name: e.Name, Perms: e.Perms}
}

// entryRefusal says why an entry may not be declared in that scope, or "" when it may.
func entryRefusal(entry acl.EntryArgs, scope acl.Scope) string {
	named := entry.Name != ""
	if entry.Type == acl.Mask {
		return "a mask is computed by setfacl from the entries around it, and declaring one would be " +
			"overwritten the next time any entry changed. Declare the entries you want and let the mask follow."
	}
	if entry.Type == acl.Other && named {
		return "an other entry names nobody, so it takes no name"
	}
	if !named && scope == acl.Access {
		bits := map[acl.EntryType]string{acl.User: "owner", acl.Group: "group", acl.Other: "other"}[entry.Type]
		if bits == "" {
			// { user, group, other }[type] is undefined for a type that is none of them
			bits = "undefined"
		}
		return string(entry.Type) + ":: is the " + bits + " mode bits under another name, and setting it here would " +
			"fight Directory's mode over the same three numbers. Set the mode there. As a defaultEntry it " +
			"is a different thing and is allowed, because no mode sets that."
	}
	return ""
}

// aclStep is one entry to set or remove, in a scope.
type aclStep struct {
	Entry acl.EntryArgs
	Scope acl.Scope
}

// setfaclCommand removes, then sets, each in one call, stopping at the first failure; "" when there
// is nothing to do. Never -b or --set, which would remove entries this resource does not own.
func setfaclCommand(path string, set, remove []aclStep) (string, error) {
	var commands []string
	if len(remove) > 0 {
		parts := make([]string, len(remove))
		for i, r := range remove {
			parts[i] = acl.FormatRemoval(r.Entry.Type, r.Entry.Name, r.Scope)
		}
		commands = append(commands, "setfacl -x "+shellQuote(strings.Join(parts, ","))+" "+shellQuote(path))
	}
	if len(set) > 0 {
		parts := make([]string, len(set))
		for i, s := range set {
			formatted, err := acl.FormatEntry(s.Entry, s.Scope)
			if err != nil {
				return "", err
			}
			parts[i] = formatted
		}
		commands = append(commands, "setfacl -m "+shellQuote(strings.Join(parts, ","))+" "+shellQuote(path))
	}
	return strings.Join(commands, " && "), nil
}

// resolvedEntries is a declaration settled: refused entries raised, names filled in, permissions
// normalised, and sorted by key as localeCompare sorts them.
type resolvedEntries struct {
	Entries        []AclEntrySettled
	DefaultEntries []AclEntrySettled
}

func resolveEntries(path string, entries, defaultEntries []AclEntryArgs) (resolvedEntries, error) {
	settle := func(list []AclEntryArgs, scope acl.Scope) ([]AclEntrySettled, error) {
		out := []AclEntrySettled{}
		for _, e := range list {
			entry := e.acl()
			if refusal := entryRefusal(entry, scope); refusal != "" {
				return nil, fmt.Errorf("%s on %s: %s", acl.FormatRemoval(entry.Type, entry.Name, scope), path, refusal)
			}
			perms, err := acl.NormalisePerms(entry.Perms)
			if err != nil {
				return nil, err
			}
			out = append(out, AclEntrySettled{Type: e.Type, Name: entry.Name, Perms: perms})
		}
		slices.SortStableFunc(out, func(a, b AclEntrySettled) int {
			return jscompat.LocaleCompare(acl.EntryKey(acl.EntryType(a.Type), a.Name), acl.EntryKey(acl.EntryType(b.Type), b.Name))
		})
		return out, nil
	}
	access, err := settle(entries, acl.Access)
	if err != nil {
		return resolvedEntries{}, err
	}
	defaults, err := settle(defaultEntries, acl.Default)
	if err != nil {
		return resolvedEntries{}, err
	}
	return resolvedEntries{Entries: access, DefaultEntries: defaults}, nil
}

// normaliseRefusal says why a normalise rule cannot be applied, or "" when it can.
func normaliseRefusal(rule AclNormaliseArgs) string {
	if jscompat.Trim(rule.Group) == "" {
		return "normalise needs the group whose directories it corrects"
	}
	if bad := mode.ModeRefusal(rule.From); bad != "" {
		return bad
	}
	if bad := mode.ModeRefusal(rule.To); bad != "" {
		return bad
	}
	if mode.NormaliseMode(rule.From) == mode.NormaliseMode(rule.To) {
		return "normalise from and to are both " + mode.NormaliseMode(rule.To) + ", so it would change nothing"
	}
	for _, ignored := range rule.Ignore {
		var parts []string
		for _, part := range strings.Split(ignored, "/") {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if strings.HasPrefix(ignored, "/") || len(parts) == 0 || slices.Contains(parts, ".") || slices.Contains(parts, "..") {
			return "normalise ignore entry '" + ignored + "' must be a path relative to the ACL's own path"
		}
	}
	return ""
}

var edgeSlashes = regexp.MustCompile(`^/+|/+$`)

// resolveNormalise settles a rule to one spelling: modes four digits, ignores trimmed of slashes and
// sorted, absent ignore as empty.
func resolveNormalise(rule AclNormaliseArgs) (AclNormaliseArgs, error) {
	if refusal := normaliseRefusal(rule); refusal != "" {
		return AclNormaliseArgs{}, errors.New(refusal)
	}
	ignore := []string{}
	for _, ignored := range rule.Ignore {
		ignore = append(ignore, edgeSlashes.ReplaceAllString(ignored, ""))
	}
	// Array.prototype.sort with no comparator: UTF-16 code unit order
	slices.SortStableFunc(ignore, jscompat.CompareUTF16)
	return AclNormaliseArgs{Group: rule.Group, From: mode.NormaliseMode(rule.From), To: mode.NormaliseMode(rule.To), Ignore: ignore}, nil
}

// normaliseChanged reports whether the applied rule and the wanted one differ once settled.
func normaliseChanged(applied, wanted *AclNormaliseArgs) (bool, error) {
	if applied == nil || wanted == nil {
		return (applied == nil) != (wanted == nil), nil
	}
	had, err := resolveNormalise(*applied)
	if err != nil {
		return false, err
	}
	want, err := resolveNormalise(*wanted)
	if err != nil {
		return false, err
	}
	return had.Group != want.Group || had.From != want.From || had.To != want.To || !slices.Equal(had.Ignore, want.Ignore), nil
}

var findPattern = regexp.MustCompile(`[\\*?\[\]]`)

// findPathLiteral escapes what find's -path would read as a pattern.
func findPathLiteral(path string) string {
	return findPattern.ReplaceAllString(path, `\$0`)
}

var trailingSlashes = regexp.MustCompile(`/+$`)

func walkCommand(path string, rule AclNormaliseArgs, action string) string {
	root := trailingSlashes.ReplaceAllString(path, "")
	ignored := make([]string, len(rule.Ignore))
	for i, entry := range rule.Ignore {
		ignored[i] = "-path " + shellQuote(findPathLiteral(root+"/"+entry))
	}
	prune := ""
	if len(ignored) > 0 {
		prune = `\( ` + strings.Join(ignored, " -o ") + ` \) -prune -o `
	}
	return "find " + shellQuote(root) + " -xdev " + prune + "-type d -group " + shellQuote(rule.Group) + " " +
		"-perm " + rule.From + " -print" + action + " | wc -l"
}

// normaliseCommand corrects the directories and prints how many it found; the walk streams on the
// machine and only the count comes back.
func normaliseCommand(path string, rule AclNormaliseArgs) string {
	return walkCommand(path, rule, " -exec chmod "+rule.To+" {} +")
}

// normaliseCheckCommand walks exactly what the correction walks, and changes nothing.
func normaliseCheckCommand(path string, rule AclNormaliseArgs) string {
	return walkCommand(path, rule, "")
}

type aclRead struct {
	access, defaults []acl.Entry
}

func readAcl(ctx context.Context, m *machine, path string) (*aclRead, error) {
	// a path that is not there is an answer rather than a fault, and 9 is not a code getfacl uses
	asked, err := m.ask(ctx, m.escalate("test -e "+shellQuote(path)+" || exit 9; getfacl -pc "+shellQuote(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code == 127 {
		return nil, fmt.Errorf("getfacl is not installed on %s — it is in the acl package, which you can "+
			"declare with new AptPackage('acl', host, { name: 'acl' })", m.describe())
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read the ACL on %s: %s", path, jscompat.Trim(asked.err))
	}
	access, defaults := acl.ParseGetfacl(asked.out)
	return &aclRead{access: access, defaults: defaults}, nil
}

func actualOf(found []acl.Entry, wanted []AclEntrySettled) []AclEntryActual {
	out := make([]AclEntryActual, len(wanted))
	for i, w := range wanted {
		e := acl.FindACLEntry(found, w.acl())
		out[i] = AclEntryActual{Type: string(e.Type), Name: e.Name, Perms: e.Perms, Effective: e.Effective}
	}
	return out
}

func settledArgs(list []AclEntrySettled) []acl.EntryArgs {
	out := make([]acl.EntryArgs, len(list))
	for i, e := range list {
		out[i] = e.acl()
	}
	return out
}

func actualEntries(list []AclEntryActual) []acl.Entry {
	out := make([]acl.Entry, len(list))
	for i, e := range list {
		out[i] = acl.Entry{Type: acl.EntryType(e.Type), Name: e.Name, Perms: e.Perms, Effective: e.Effective}
	}
	return out
}

func settleAcl(ctx context.Context, m *machine, args PosixAclArgs, previous *PosixAclState) (PosixAclState, error) {
	wanted, err := resolveEntries(args.Path, args.Entries, args.DefaultEntries)
	if err != nil {
		return PosixAclState{}, err
	}
	before, err := readAcl(ctx, m, args.Path)
	if err != nil {
		return PosixAclState{}, err
	}
	if before == nil {
		return PosixAclState{}, fmt.Errorf("nothing exists at %s on %s, so there is no ACL to set on it", args.Path, m.describe())
	}

	var set, remove []aclStep
	for _, pair := range []struct {
		found  []acl.Entry
		wanted []AclEntrySettled
		scope  acl.Scope
	}{{before.access, wanted.Entries, acl.Access}, {before.defaults, wanted.DefaultEntries, acl.Default}} {
		changes, err := acl.Unsatisfied(pair.found, settledArgs(pair.wanted), pair.scope)
		if err != nil {
			return PosixAclState{}, err
		}
		for _, c := range changes {
			set = append(set, aclStep{Entry: c.Entry, Scope: c.Scope})
		}
	}
	var prevEntries, prevDefaults []AclEntrySettled
	if previous != nil {
		prevEntries, prevDefaults = previous.Entries, previous.DefaultEntries
	}
	for _, e := range acl.Abandoned(settledArgs(prevEntries), settledArgs(wanted.Entries)) {
		remove = append(remove, aclStep{Entry: e, Scope: acl.Access})
	}
	for _, e := range acl.Abandoned(settledArgs(prevDefaults), settledArgs(wanted.DefaultEntries)) {
		remove = append(remove, aclStep{Entry: e, Scope: acl.Default})
	}

	command, err := setfaclCommand(args.Path, set, remove)
	if err != nil {
		return PosixAclState{}, err
	}
	if command != "" {
		if _, err := m.must(ctx, m.escalate(command)); err != nil {
			return PosixAclState{}, err
		}
	}

	after, err := readAcl(ctx, m, args.Path)
	if err != nil {
		return PosixAclState{}, err
	}
	if after == nil {
		return PosixAclState{}, fmt.Errorf("%s on %s disappeared while its ACL was being set", args.Path, m.describe())
	}
	actual := actualOf(after.access, wanted.Entries)
	defaultActual := actualOf(after.defaults, wanted.DefaultEntries)

	var stillWrong []string
	for _, pair := range []struct {
		wanted []AclEntrySettled
		actual []AclEntryActual
		scope  acl.Scope
	}{{wanted.Entries, actual, acl.Access}, {wanted.DefaultEntries, defaultActual, acl.Default}} {
		found := actualEntries(pair.actual)
		for i, w := range pair.wanted {
			ok, err := acl.EntrySatisfied(found[i], w.acl())
			if err != nil {
				return PosixAclState{}, err
			}
			if !ok {
				formatted, _ := acl.FormatEntry(w.acl(), pair.scope)
				stillWrong = append(stillWrong, formatted)
			}
		}
	}
	if len(stillWrong) > 0 {
		return PosixAclState{}, fmt.Errorf("set %s on %s at %s and the ACL still does not "+
			"grant it — a mask is suppressing it, which a chmod after this would explain",
			strings.Join(stillWrong, ", "), args.Path, m.describe())
	}

	state := PosixAclState{Path: args.Path, Entries: wanted.Entries, DefaultEntries: wanted.DefaultEntries, Actual: actual, DefaultActual: defaultActual}
	if args.Normalise == nil {
		return state, nil
	}
	rule, err := resolveNormalise(*args.Normalise)
	if err != nil {
		return PosixAclState{}, err
	}
	changed, err := m.must(ctx, m.escalate(normaliseCommand(args.Path, rule)))
	if err != nil {
		return PosixAclState{}, err
	}
	// the pipe into wc hides a failed chmod, so what counts is whether any are left
	left, err := m.must(ctx, m.escalate(normaliseCheckCommand(args.Path, rule)))
	if err != nil {
		return PosixAclState{}, err
	}
	if jscompat.Number(left) != 0 {
		return PosixAclState{}, fmt.Errorf("%s directories under %s at %s are still %s after being set to %s",
			jscompat.Trim(left), args.Path, m.describe(), rule.From, rule.To)
	}
	normalised := int(jscompat.Number(changed))
	state.Normalise, state.Normalised = &rule, &normalised
	return state, nil
}

// Check refuses entries and normalise rules that cannot be declared, at preview.
func (PosixAcl) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[PosixAclArgs], error) {
	args, failures, err := infer.DefaultCheck[PosixAclArgs](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[PosixAclArgs]{Inputs: args, Failures: failures}, err
	}
	for _, pair := range []struct {
		entries  []AclEntryArgs
		scope    acl.Scope
		property string
	}{{args.Entries, acl.Access, "entries"}, {args.DefaultEntries, acl.Default, "defaultEntries"}} {
		for _, e := range pair.entries {
			entry := e.acl()
			if refusal := entryRefusal(entry, pair.scope); refusal != "" {
				failures = append(failures, p.CheckFailure{Property: pair.property,
					Reason: acl.FormatRemoval(entry.Type, entry.Name, pair.scope) + ": " + refusal})
			}
		}
	}
	if args.Normalise != nil {
		if bad := normaliseRefusal(*args.Normalise); bad != "" {
			failures = append(failures, p.CheckFailure{Property: "normalise", Reason: bad})
		}
	}
	return infer.CheckResponse[PosixAclArgs]{Inputs: args, Failures: failures}, nil
}

func previewAcl(args PosixAclArgs) (PosixAclState, error) {
	wanted, err := resolveEntries(args.Path, args.Entries, args.DefaultEntries)
	if err != nil {
		return PosixAclState{}, err
	}
	return PosixAclState{Path: args.Path, Entries: wanted.Entries, DefaultEntries: wanted.DefaultEntries,
		Actual: []AclEntryActual{}, DefaultActual: []AclEntryActual{}}, nil
}

// Create sets the declared entries.
func (PosixAcl) Create(ctx context.Context, req infer.CreateRequest[PosixAclArgs]) (infer.CreateResponse[PosixAclState], error) {
	if req.DryRun {
		state, err := previewAcl(req.Inputs)
		return infer.CreateResponse[PosixAclState]{ID: req.Inputs.Path, Output: state}, err
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[PosixAclState]{}, err
	}
	state, err := settleAcl(ctx, m, req.Inputs, nil)
	if err != nil {
		return infer.CreateResponse[PosixAclState]{}, err
	}
	return infer.CreateResponse[PosixAclState]{ID: req.Inputs.Path, Output: state}, nil
}

// Read refreshes what the machine grants for each declared entry; a path gone from the machine is
// forgotten rather than reported as an ACL on nothing.
func (PosixAcl) Read(ctx context.Context, req infer.ReadRequest[PosixAclArgs, PosixAclState]) (infer.ReadResponse[PosixAclArgs, PosixAclState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[PosixAclArgs, PosixAclState]{}, err
	}
	actual, err := readAcl(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[PosixAclArgs, PosixAclState]{}, err
	}
	if actual == nil {
		return infer.ReadResponse[PosixAclArgs, PosixAclState]{}, nil
	}
	state := req.State
	inputs := req.Inputs
	if state.Path == "" {
		// an import: nothing remembered about what this resource owns
		state, inputs, err = importedAcl(req.ID, req.Inputs, actual)
		if err != nil {
			return infer.ReadResponse[PosixAclArgs, PosixAclState]{}, err
		}
		return infer.ReadResponse[PosixAclArgs, PosixAclState]{ID: req.ID, Inputs: inputs, State: state}, nil
	}
	if state.Entries == nil {
		state.Entries = []AclEntrySettled{}
	}
	if state.DefaultEntries == nil {
		state.DefaultEntries = []AclEntrySettled{}
	}
	if adopted(state) {
		state.Actual, state.DefaultActual = everyEntry(actual)
	} else {
		state.Actual = actualOf(actual.access, state.Entries)
		state.DefaultActual = actualOf(actual.defaults, state.DefaultEntries)
	}
	return infer.ReadResponse[PosixAclArgs, PosixAclState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// adopted reports a state made by an import that was not given a declaration: it owns no entries
// yet, and its actual lists record every entry the path had, so that the program's declaration can
// be checked against them. Every other state has one actual entry per declared entry.
func adopted(s PosixAclState) bool {
	return len(s.Actual) != len(s.Entries) || len(s.DefaultActual) != len(s.DefaultEntries)
}

// everyEntry is every named access entry and every default entry on the path: the entries a
// declaration could own, as getfacl reports them.
func everyEntry(found *aclRead) (access, defaults []AclEntryActual) {
	access, defaults = []AclEntryActual{}, []AclEntryActual{}
	for _, e := range found.access {
		if (e.Type == acl.User || e.Type == acl.Group) && e.Name != "" {
			access = append(access, AclEntryActual{Type: string(e.Type), Name: e.Name, Perms: e.Perms, Effective: e.Effective})
		}
	}
	for _, e := range found.defaults {
		if e.Type != acl.Mask {
			defaults = append(defaults, AclEntryActual{Type: string(e.Type), Name: e.Name, Perms: e.Perms, Effective: e.Effective})
		}
	}
	return access, defaults
}

// importedAcl is the state and inputs an import establishes.
//
// Given the declaration (a read with inputs), it owns exactly the declared entries and projects
// what getfacl grants onto them, as the TypeScript read projected live grants onto the saved
// declaration; an already-satisfied declaration then diffs as no change, and nothing else on the
// path becomes this resource's. Given only the id, it owns nothing: the inputs list every entry the
// path has, for the import's generated code, and the diff checks the program's declaration against
// what the path grants, so a declaration the path already satisfies diffs as no change, and an
// update after it removes nothing, because nothing was owned before.
func importedAcl(id string, given PosixAclArgs, found *aclRead) (PosixAclState, PosixAclArgs, error) {
	if given.Path != "" || len(given.Entries) > 0 || len(given.DefaultEntries) > 0 || given.Normalise != nil {
		wanted, err := resolveEntries(id, given.Entries, given.DefaultEntries)
		if err != nil {
			return PosixAclState{}, PosixAclArgs{}, err
		}
		state := PosixAclState{Path: id, Entries: wanted.Entries, DefaultEntries: wanted.DefaultEntries,
			Actual: actualOf(found.access, wanted.Entries), DefaultActual: actualOf(found.defaults, wanted.DefaultEntries)}
		if given.Normalise != nil {
			rule, err := resolveNormalise(*given.Normalise)
			if err != nil {
				return PosixAclState{}, PosixAclArgs{}, err
			}
			state.Normalise = &rule
		}
		given.Path = id
		return state, given, nil
	}
	access, defaults := everyEntry(found)
	state := PosixAclState{Path: id, Entries: []AclEntrySettled{}, DefaultEntries: []AclEntrySettled{}, Actual: access, DefaultActual: defaults}
	inputs := PosixAclArgs{Path: id}
	for _, e := range access {
		inputs.Entries = append(inputs.Entries, declaredOf(e))
	}
	for _, e := range defaults {
		inputs.DefaultEntries = append(inputs.DefaultEntries, declaredOf(e))
	}
	return state, inputs, nil
}

func declaredOf(e AclEntryActual) AclEntryArgs {
	d := AclEntryArgs{Type: e.Type, Perms: e.Perms}
	if e.Name != "" {
		name := e.Name
		d.Name = &name
	}
	return d
}

// Update sets what is declared and removes what was declared before and no longer is.
func (PosixAcl) Update(ctx context.Context, req infer.UpdateRequest[PosixAclArgs, PosixAclState]) (infer.UpdateResponse[PosixAclState], error) {
	args := req.Inputs
	args.Path = req.ID
	if req.DryRun {
		state, err := previewAcl(args)
		return infer.UpdateResponse[PosixAclState]{Output: state}, err
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[PosixAclState]{}, err
	}
	old := req.State
	state, err := settleAcl(ctx, m, args, &old)
	if err != nil {
		return infer.UpdateResponse[PosixAclState]{}, err
	}
	return infer.UpdateResponse[PosixAclState]{Output: state}, nil
}

// Diff: the declared entries or rule changing, or the machine no longer granting what was asked
// for (a chmod that recomputed the mask), is an update; a new path replaces, deleting first.
func (PosixAcl) Diff(_ context.Context, req infer.DiffRequest[PosixAclArgs, PosixAclState]) (p.DiffResponse, error) {
	old := req.State
	wanted, err := resolveEntries(old.Path, req.Inputs.Entries, req.Inputs.DefaultEntries)
	if err != nil {
		return p.DiffResponse{}, err
	}
	declared := func(list []AclEntrySettled) string {
		parts := make([]string, len(list))
		for i, e := range list {
			parts[i], _ = acl.FormatEntry(e.acl(), acl.Access)
		}
		return strings.Join(parts, ",")
	}
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if adopted(old) {
		// imported without a declaration: no change while the path grants everything declared
		for _, pair := range []struct {
			found    []AclEntryActual
			wanted   []AclEntrySettled
			scope    acl.Scope
			property string
		}{{old.Actual, wanted.Entries, acl.Access, "entries"}, {old.DefaultActual, wanted.DefaultEntries, acl.Default, "defaultEntries"}} {
			missing, err := acl.Unsatisfied(actualEntries(pair.found), settledArgs(pair.wanted), pair.scope)
			if err != nil {
				return p.DiffResponse{}, err
			}
			if len(missing) > 0 {
				resp.DetailedDiff[pair.property] = p.PropertyDiff{Kind: p.Update}
			}
		}
		if req.Inputs.Normalise != nil {
			resp.DetailedDiff["normalise"] = p.PropertyDiff{Kind: p.Update}
		}
		if old.Path != req.Inputs.Path {
			resp.DetailedDiff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
		}
		resp.HasChanges = len(resp.DetailedDiff) > 0
		return resp, nil
	}
	drift, err := acl.Unsatisfied(actualEntries(old.Actual), settledArgs(old.Entries), acl.Access)
	if err != nil {
		return p.DiffResponse{}, err
	}
	if declared(old.Entries) != declared(wanted.Entries) || len(drift) > 0 {
		resp.DetailedDiff["entries"] = p.PropertyDiff{Kind: p.Update}
	}
	defaultDrift, err := acl.Unsatisfied(actualEntries(old.DefaultActual), settledArgs(old.DefaultEntries), acl.Default)
	if err != nil {
		return p.DiffResponse{}, err
	}
	if declared(old.DefaultEntries) != declared(wanted.DefaultEntries) || len(defaultDrift) > 0 {
		resp.DetailedDiff["defaultEntries"] = p.PropertyDiff{Kind: p.Update}
	}
	changed, err := normaliseChanged(old.Normalise, req.Inputs.Normalise)
	if err != nil {
		return p.DiffResponse{}, err
	}
	if changed {
		resp.DetailedDiff["normalise"] = p.PropertyDiff{Kind: p.Update}
	}
	if old.Path != req.Inputs.Path {
		resp.DetailedDiff["path"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete removes only what this resource declared, never setfacl -b: the entries somebody else put
// on a shared pool are not this resource's to take away.
func (PosixAcl) Delete(ctx context.Context, req infer.DeleteRequest[PosixAclState]) (infer.DeleteResponse, error) {
	path := req.State.Path
	if path == "" {
		path = req.ID
	}
	var remove []aclStep
	for _, e := range req.State.Entries {
		remove = append(remove, aclStep{Entry: e.acl(), Scope: acl.Access})
	}
	for _, e := range req.State.DefaultEntries {
		remove = append(remove, aclStep{Entry: e.acl(), Scope: acl.Default})
	}
	command, err := setfaclCommand(path, nil, remove)
	if err != nil || command == "" {
		return infer.DeleteResponse{}, err
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(command))
	return infer.DeleteResponse{}, err
}
