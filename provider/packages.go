package homelab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/apt"
	"github.com/antimatter-studios/pulumi-homelab/provider/aptmark"
)

// AptPackages is the list of packages a machine should have beyond what the image came with, and
// the ones it should not. Ported from src/resources/packages.ts.
//
// It asks dpkg once for all of them and installs the missing ones in one apt-get. It adds and it
// never removes on delete: taking a name out of the list leaves the package installed, because
// purging an inventory can cascade into packages nothing here mentions. What is declared absent is
// purged, but only after a simulation shows apt would take nothing else with it. What is declared
// present is marked manual, so an autoremove aimed at something else cannot take it.
type AptPackages struct{}

// AptPackagesArgs are the inputs of an AptPackages.
type AptPackagesArgs struct {
	Present []string `pulumi:"present,optional"`
	Absent  []string `pulumi:"absent,optional"`
	Names   []string `pulumi:"names,optional"`
	Update  *bool    `pulumi:"update,optional"`
}

// AptPackagesState is what is kept in state. present, names and update are optional so that a
// state that never recorded them reads as changed rather than as agreeing (see packagesChanged).
type AptPackagesState struct {
	Present   []string          `pulumi:"present,optional"`
	Absent    []string          `pulumi:"absent,optional"`
	Names     []string          `pulumi:"names,optional"`
	Update    *bool             `pulumi:"update,optional"`
	Installed map[string]string `pulumi:"installed"`
	Missing   []string          `pulumi:"missing"`
	Lingering []string          `pulumi:"lingering"`
	Auto      []string          `pulumi:"auto"`
}

func (a *AptPackagesArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Present, "Packages that should be installed.")
	an.Describe(&a.Absent, "Packages that should not be installed. Purged only when apt would take nothing else with them.")
	an.Describe(&a.Names, "Accepted as a synonym for present, which is what it was called first.")
	an.Describe(&a.Update, "Whether to refresh the package lists first.")
}

func (s *AptPackagesState) Annotate(an infer.Annotator) {
	an.Describe(&s.Installed, "What dpkg says is installed, name to version, for the names asked about.")
	an.Describe(&s.Missing, "Names declared present that dpkg does not have. Empty on a machine that matches the code.")
	an.Describe(&s.Lingering, "Names declared absent that dpkg still has. Empty on a machine that matches the code.")
	an.Describe(&s.Auto, "Names declared present that apt holds as auto rather than manual, which an autoremove is entitled to take.")
}

func (r *AptPackages) Annotate(an infer.Annotator) {
	an.Describe(&r, "The list of packages a machine should have, checked against dpkg rather than remembered.")
	an.SetToken("index", "AptPackages")
}

// readPackageState is what dpkg knows about the requested names and how apt holds them.
func readPackageState(ctx context.Context, m *machine, names []string) (apt.PackageState, error) {
	if len(names) == 0 {
		return apt.PackageState{Installed: map[string]string{}, Manual: []string{}}, nil
	}
	quoted := quoteAll(names)
	// Exit 1 can carry a partial answer when only some packages are known.
	dpkg, err := m.commandResult(ctx, "dpkg-query", "dpkg-query -W -f='${binary:Package} ${db:Status-Status} ${Version}\\n' -- "+quoted, 1)
	if err != nil {
		return apt.PackageState{}, err
	}
	marked, err := m.commandResult(ctx, "apt-mark showmanual", "apt-mark showmanual -- "+quoted)
	if err != nil {
		return apt.PackageState{}, err
	}
	return apt.PackageState{Installed: apt.ParseInstalled(dpkg.out), Manual: aptmark.ParseManual(marked.out)}, nil
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = shellQuote(name)
	}
	return strings.Join(quoted, " ")
}

// presentCommands are the commands that bring the declared-present half into line, in order:
// refresh, install, then mark. Purging is not in here: it carries its own simulation guard.
func presentCommands(missing, auto []string, update bool) []string {
	commands := []string{}
	if update {
		commands = append(commands, aptGet+" update -qq")
	}
	if len(missing) > 0 {
		// one apt-get for the whole set, and noninteractive because a package with a configuration
		// prompt otherwise hangs the deployment behind a dialogue nobody can see
		commands = append(commands, "DEBIAN_FRONTEND=noninteractive "+aptGet+" install -y -qq "+quoteAll(missing))
	}
	if mark, ok := aptmark.MarkManualCommand(auto); ok {
		commands = append(commands, mark)
	}
	return commands
}

// packagesRecord is what a state says, with nil standing for a field it never recorded, as
// TypeScript's undefined does.
type packagesRecord struct {
	Present, Absent, Names   []string
	Update                   *bool
	Missing, Lingering, Auto []string
}

type packagesWanted struct {
	Present, Absent []string
	Update          bool
}

// packagesChanged is whether the machine has stopped matching the declaration. A state with
// nothing recorded is changed rather than matching.
func packagesChanged(old packagesRecord, next packagesWanted) bool {
	present := old.Present
	if present == nil {
		present = old.Names
	}
	return len(old.Missing) > 0 ||
		len(old.Lingering) > 0 ||
		// a declared package apt holds as auto is present and still removable by an autoremove nobody
		// aimed at it, so the declaration is weaker than it reads until this is fixed
		len(old.Auto) > 0 ||
		strings.Join(present, ",") != strings.Join(next.Present, ",") ||
		strings.Join(old.Absent, ",") != strings.Join(next.Absent, ",") ||
		old.Update == nil || *old.Update != next.Update
}

// missingFrom is the names dpkg does not report as installed, in the order they were declared. A
// multi-arch package reported as name:arch counts as present under its bare name.
func missingFrom(names []string, installed map[string]string) []string {
	present := map[string]bool{}
	for name := range installed {
		present[strings.Split(name, ":")[0]] = true
	}
	missing := []string{}
	for _, name := range names {
		if !present[name] {
			missing = append(missing, name)
		}
	}
	return missing
}

// lingeringFrom is the names declared absent that dpkg still has.
func lingeringFrom(absent []string, installed map[string]string) []string {
	lingering := []string{}
	for _, name := range absent {
		if len(missingFrom([]string{name}, installed)) == 0 {
			lingering = append(lingering, name)
		}
	}
	return lingering
}

// wantedPackages is both lists, however they were spelled, after the contradiction check.
func wantedPackages(args AptPackagesArgs) (packagesWanted, error) {
	present := args.Present
	if present == nil {
		present = args.Names
	}
	if present == nil {
		present = []string{}
	}
	absent := args.Absent
	if absent == nil {
		absent = []string{}
	}
	both := []string{}
	for _, name := range present {
		if slices.Contains(absent, name) {
			both = append(both, name)
		}
	}
	if len(both) > 0 {
		return packagesWanted{}, fmt.Errorf("declared both present and absent, which is not a machine that can exist: %s", strings.Join(both, ", "))
	}
	return packagesWanted{Present: present, Absent: absent, Update: deref(args.Update)}, nil
}

// purgeRefusal says why purging names is refused, given what apt's simulation would remove.
func purgeRefusal(names, simulated []string) string {
	collateral := []string{}
	for _, name := range simulated {
		bare := strings.Split(name, ":")[0]
		if !slices.Contains(names, bare) {
			collateral = append(collateral, bare)
		}
	}
	if len(collateral) == 0 {
		return ""
	}
	return fmt.Sprintf("refusing to purge %s: apt would also remove %s. ", strings.Join(names, ", "), strings.Join(collateral, ", ")) +
		"Declaring a package absent is a statement about that package, not permission to remove what depends on it."
}

// purgePackages takes the named packages off the machine, or refuses and says what apt would have
// taken with them.
func purgePackages(ctx context.Context, m *machine, names []string) error {
	if len(names) == 0 {
		return nil
	}
	quoted := quoteAll(names)
	simulated, err := m.must(ctx, m.escalate("apt-get -s purge "+quoted+" 2>/dev/null"))
	if err != nil {
		return err
	}
	if refusal := purgeRefusal(names, apt.ParseSimulatedRemovals(simulated)); refusal != "" {
		return errors.New(refusal)
	}
	_, err = m.must(ctx, m.escalate("DEBIAN_FRONTEND=noninteractive "+aptGet+" purge -y -qq "+quoted))
	return err
}

func settlePackages(ctx context.Context, m *machine, args AptPackagesArgs) (AptPackagesState, error) {
	want, err := wantedPackages(args)
	if err != nil {
		return AptPackagesState{}, err
	}
	all := append(slices.Clone(want.Present), want.Absent...)
	before, err := readPackageState(ctx, m, all)
	if err != nil {
		return AptPackagesState{}, err
	}
	// only the difference: a write on every deployment is a line in every log for a machine nobody
	// touched
	commands := presentCommands(missingFrom(want.Present, before.Installed),
		aptmark.AutoMarked(want.Present, before.Installed, before.Manual), want.Update)
	if len(commands) > 0 {
		if _, err := m.must(ctx, m.escalate(strings.Join(commands, " && "))); err != nil {
			return AptPackagesState{}, err
		}
	}
	// installs first, then removals
	if err := purgePackages(ctx, m, lingeringFrom(want.Absent, before.Installed)); err != nil {
		return AptPackagesState{}, err
	}

	after, err := readPackageState(ctx, m, all)
	if err != nil {
		return AptPackagesState{}, err
	}
	if still := missingFrom(want.Present, after.Installed); len(still) > 0 {
		return AptPackagesState{}, fmt.Errorf("apt said it installed them, but dpkg cannot find: %s", strings.Join(still, ", "))
	}
	if still := lingeringFrom(want.Absent, after.Installed); len(still) > 0 {
		return AptPackagesState{}, fmt.Errorf("apt said it removed them, but dpkg still has: %s", strings.Join(still, ", "))
	}
	if still := aptmark.AutoMarked(want.Present, after.Installed, after.Manual); len(still) > 0 {
		return AptPackagesState{}, fmt.Errorf("apt-mark said it marked them manual, but apt still holds them as auto: %s", strings.Join(still, ", "))
	}
	update := want.Update
	return AptPackagesState{
		Present: want.Present, Absent: want.Absent, Names: want.Present, Update: &update,
		Installed: after.Installed, Missing: []string{}, Lingering: []string{}, Auto: []string{},
	}, nil
}

// Create settles the list. The id is fixed: this resource is "the list", and a list that changed
// its identity whenever a package was added would replace itself.
func (AptPackages) Create(ctx context.Context, req infer.CreateRequest[AptPackagesArgs]) (infer.CreateResponse[AptPackagesState], error) {
	if req.DryRun {
		want, err := wantedPackages(req.Inputs)
		if err != nil {
			return infer.CreateResponse[AptPackagesState]{}, err
		}
		return infer.CreateResponse[AptPackagesState]{ID: "apt-packages", Output: AptPackagesState{
			Present: want.Present, Absent: want.Absent, Names: want.Present, Update: &want.Update,
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[AptPackagesState]{}, err
	}
	state, err := settlePackages(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[AptPackagesState]{}, err
	}
	return infer.CreateResponse[AptPackagesState]{ID: "apt-packages", Output: state}, nil
}

// Read reports drift rather than forgetting: a package removed by hand, or installed against a
// declaration that it should not be, means the list has stopped being true, not that it is gone.
func (AptPackages) Read(ctx context.Context, req infer.ReadRequest[AptPackagesArgs, AptPackagesState]) (infer.ReadResponse[AptPackagesArgs, AptPackagesState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[AptPackagesArgs, AptPackagesState]{}, err
	}
	state := req.State
	if state.Present == nil && state.Names == nil && state.Absent == nil && state.Update == nil {
		state.Present, state.Names, state.Absent, state.Update = req.Inputs.Present, req.Inputs.Names, req.Inputs.Absent, req.Inputs.Update
	}
	present := state.Present
	if present == nil {
		present = state.Names
	}
	if present == nil {
		present = []string{}
	}
	absent := state.Absent
	if absent == nil {
		absent = []string{}
	}
	read, err := readPackageState(ctx, m, append(slices.Clone(present), absent...))
	if err != nil {
		return infer.ReadResponse[AptPackagesArgs, AptPackagesState]{}, err
	}
	if state.Update == nil {
		update := false
		state.Update = &update
	}
	state.Present, state.Absent, state.Names = present, absent, present
	state.Installed = read.Installed
	state.Missing = missingFrom(present, read.Installed)
	state.Lingering = lingeringFrom(absent, read.Installed)
	state.Auto = aptmark.AutoMarked(present, read.Installed, read.Manual)
	inputs := req.Inputs
	if inputs.Present == nil && inputs.Names == nil && inputs.Absent == nil && inputs.Update == nil {
		// an import: the declaration is whatever the state recorded
		inputs = AptPackagesArgs{Present: present, Absent: absent, Update: state.Update}
	}
	return infer.ReadResponse[AptPackagesArgs, AptPackagesState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the list again.
func (AptPackages) Update(ctx context.Context, req infer.UpdateRequest[AptPackagesArgs, AptPackagesState]) (infer.UpdateResponse[AptPackagesState], error) {
	if req.DryRun {
		want, err := wantedPackages(req.Inputs)
		if err != nil {
			return infer.UpdateResponse[AptPackagesState]{}, err
		}
		out := req.State
		out.Present, out.Absent, out.Names, out.Update = want.Present, want.Absent, want.Present, &want.Update
		return infer.UpdateResponse[AptPackagesState]{Output: out}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[AptPackagesState]{}, err
	}
	state, err := settlePackages(ctx, m, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[AptPackagesState]{}, err
	}
	return infer.UpdateResponse[AptPackagesState]{Output: state}, nil
}

// Diff: drift in either direction counts, and so does any change to the declaration. Nothing
// replaces.
func (AptPackages) Diff(_ context.Context, req infer.DiffRequest[AptPackagesArgs, AptPackagesState]) (p.DiffResponse, error) {
	want, err := wantedPackages(req.Inputs)
	if err != nil {
		return p.DiffResponse{}, err
	}
	s := req.State
	changed := packagesChanged(packagesRecord{
		Present: s.Present, Absent: s.Absent, Names: s.Names, Update: s.Update,
		Missing: s.Missing, Lingering: s.Lingering, Auto: s.Auto,
	}, want)
	return p.DiffResponse{HasChanges: changed}, nil
}

// Delete does nothing, in either direction. Removing the description of a machine's package list
// is not a request to strip the machine, nor to reinstall what was declared absent.
func (AptPackages) Delete(context.Context, infer.DeleteRequest[AptPackagesState]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, nil
}
