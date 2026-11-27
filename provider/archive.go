package homelab

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/archive"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
)

// Archive is software installed once from a verified archive, and thereafter left alone. Ported
// from src/resources/archive.ts.
//
// The url is a bootstrap, not a version policy: this asks one question, is it installed, and
// fetches only when the answer is no or when the program's description of the source changes. It
// never compares versions, so software that updates itself is not drift. The checksum is required.
// The owner decides whether self-updating software can rewrite its own tree, and a wrong owner is
// fixed with a chown rather than a refetch.
type Archive struct{}

// ArchiveArgs are the inputs of an Archive.
type ArchiveArgs struct {
	Name           string  `pulumi:"name"`
	URL            *string `pulumi:"url"`
	Sha256         *string `pulumi:"sha256"`
	Prefix         string  `pulumi:"prefix"`
	Link           *string `pulumi:"link,optional"`
	Binary         *string `pulumi:"binary,optional"`
	HealthCommand  *string `pulumi:"healthCommand,optional"`
	VersionCommand *string `pulumi:"versionCommand,optional"`
	VersionPattern *string `pulumi:"versionPattern,optional"`
	Owner          *string `pulumi:"owner,optional"`
	Group          *string `pulumi:"group,optional"`
	Strip          *int    `pulumi:"strip,optional"`
	Format         *string `pulumi:"format,optional"`
}

// ArchiveState is what is kept in state: the settled arguments and what the machine said.
type ArchiveState struct {
	Name           string  `pulumi:"name"`
	URL            *string `pulumi:"url,optional"`
	Sha256         *string `pulumi:"sha256,optional"`
	Prefix         string  `pulumi:"prefix"`
	Link           *string `pulumi:"link,optional"`
	Binary         *string `pulumi:"binary,optional"`
	HealthCommand  *string `pulumi:"healthCommand,optional"`
	Owner          string  `pulumi:"owner"`
	Group          string  `pulumi:"group"`
	Strip          *int    `pulumi:"strip,optional"`
	Format         *string `pulumi:"format,optional"`
	VersionCommand string  `pulumi:"versionCommand"`
	VersionPattern *string `pulumi:"versionPattern,optional"`
	Installed      bool    `pulumi:"installed"`
	Version        string  `pulumi:"version"`
}

func (a *ArchiveArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "What it is, for the id and for messages.")
	an.Describe(&a.URL, "Where the archive is.")
	an.Describe(&a.Sha256, "SHA-256 of the archive, and not optional. A checksum nobody set is a checksum nobody checked.")
	an.Describe(&a.Prefix, "Where it unpacks.")
	an.Describe(&a.Link, "A stable path symlinked at prefix, so everything else refers to one place across upgrades.")
	an.Describe(&a.Binary, "Something under the install that has to exist and be executable for it to count as installed. A relative path is taken from link when there is one and prefix otherwise.")
	an.Describe(&a.HealthCommand, "Something that exits zero when the install works, whose output is discarded. {} expands to the link if there is one and the prefix otherwise.")
	an.Describe(&a.VersionCommand, "How to ask the software what version it is, purely for display. Reported, never compared. {} expands as for healthCommand.")
	an.Describe(&a.VersionPattern, "A JavaScript regular expression with one capture group, matched against what versionCommand prints. The pattern must compile under RE2.")
	an.Describe(&a.Owner, "Who owns the unpacked tree. Defaults to root.")
	an.Describe(&a.Group, "The group on the unpacked tree. Defaults to root.")
	an.Describe(&a.Strip, "Leading path components to strip.")
	an.Describe(&a.Format, "tar.gz, tar.xz, tar.bz2 or zip. Inferred from the url when not given.")
}

func (s *ArchiveState) Annotate(an infer.Annotator) {
	an.Describe(&s.Installed, "Whether it is there: the read, and the only thing this resource acts on.")
	an.Describe(&s.Version, "What the software says it is, or the empty string when it could not be asked. Nothing compares it.")
	an.Describe(&s.Owner, "Who owns the prefix itself, as the machine reports it after a read and as declared after an apply.")
}

func (r *Archive) Annotate(an infer.Annotator) {
	an.Describe(&r, "Software from a verified archive, installed once and thereafter left to manage itself.")
	an.SetToken("index", "Archive")
}

const (
	archiveDefaultOwner = "root"
	archiveDefaultGroup = "root"
	ownerMarker         = "#pulumi-homelab#owner"
	versionMarker       = "#pulumi-homelab#version"
)

// extractCommand unpacks one archive into a directory. unzip has no --strip-components, so a
// non-zero strip on a zip is refused rather than silently ignored.
func extractCommand(format archive.Format, file, into string, strip int) (string, error) {
	source, target := shellQuote(file), shellQuote(into)
	if format == archive.Zip {
		if strip != 0 {
			return "", fmt.Errorf("unzip has no --strip-components, so strip: %d cannot be honoured for a zip", strip)
		}
		return "unzip -q " + source + " -d " + target, nil
	}
	flag, ok := map[archive.Format]string{archive.TarGz: "-z", archive.TarXz: "-J", archive.TarBz2: "-j"}[format]
	if !ok {
		flag = "undefined"
	}
	stripping := ""
	if strip > 0 {
		stripping = " --strip-components=" + strconv.Itoa(strip)
	}
	// --no-same-owner because tar run as root restores the uids recorded in the archive by default,
	// which lets whoever built the tarball choose who owns files on this machine
	return "tar " + flag + " --no-same-owner -xf " + source + " -C " + target + stripping, nil
}

// stagingLiteral finds a staging path quoted as a literal, which has to be a shell variable.
var stagingLiteral = regexp.MustCompile(`'(\$dir[^']*)'`)

// archiveSource is what to fetch and where to put it. A change to any of it is the one reason to
// install again.
type archiveSource struct {
	URL, Sha256, Prefix, Link string
	Strip                     int
	Format                    archive.Format
}

// resolvedArchive is everything the arguments settle to, with the defaults applied exactly once.
type resolvedArchive struct {
	archiveSource
	Name, Binary, HealthCommand, Owner, Group, VersionCommand, VersionPattern, Where string
}

// archiveInstallScript downloads, verifies, unpacks into staging, moves into place, chowns, and only
// then moves the link. The order is the substance.
func archiveInstallScript(src archiveSource, owner, group string) (string, error) {
	extract, err := extractCommand(src.Format, "$dir/archive", "$dir/unpacked", src.Strip)
	if err != nil {
		return "", err
	}
	prefix := shellQuote(src.Prefix)
	steps := []string{
		"set -e",
		"dir=$(mktemp -d)",
		`trap "rm -rf \"$dir\"" EXIT`,
		`curl -fsSL --retry 3 -o "$dir/archive" ` + shellQuote(src.URL),
		"echo " + shellQuote(src.Sha256) + `"  $dir/archive" | sha256sum -c - >/dev/null`,
		`mkdir -p "$dir/unpacked"`,
		// the paths inside the staging directory are shell variables and must not be quoted as literals
		stagingLiteral.ReplaceAllString(extract, `"$1"`),
		// the move is what makes the install atomic: everything above happens where nothing looks
		"rm -rf " + prefix,
		`mkdir -p "$(dirname ` + prefix + `)"`,
		`mv "$dir/unpacked" ` + prefix,
		// recursive here and only here: the account has to be able to rewrite every file its updater
		// touches, and this is the one moment the tree is known to be exactly what was unpacked
		chownCommand(src.Prefix, owner, group),
	}
	if src.Link != "" {
		// -n as well as -f: without it, relinking a link at a directory writes the new link inside it
		steps = append(steps, "ln -sfn "+prefix+" "+shellQuote(src.Link))
		// -h, so the link itself is chowned rather than what it points at
		steps = append(steps, "chown -h "+shellQuote(owner+":"+group)+" "+shellQuote(src.Link))
	}
	return strings.Join(steps, "; "), nil
}

// chownCommand sets ownership on an unpacked tree, recursively, because an updater rewrites files
// inside it.
func chownCommand(prefix, owner, group string) string {
	return "chown -R " + shellQuote(owner+":"+group) + " " + shellQuote(prefix)
}

// ownershipChanged is whether the prefix is owned by somebody other than the declaration says.
// Nothing read back is an absent install, which the presence check already answers.
func ownershipChanged(actualOwner, actualGroup, wantedOwner, wantedGroup string) bool {
	if actualOwner == "" && actualGroup == "" {
		return false
	}
	return actualOwner != wantedOwner || actualGroup != wantedGroup
}

// whereOf is where the install is looked for: the stable link when there is one, the prefix
// otherwise.
func whereOf(prefix, link string) string {
	if link != "" {
		return link
	}
	return prefix
}

var trailingSlashes = regexp.MustCompile(`/+$`)
var leadingDotSlashes = regexp.MustCompile(`^\.?/+`)

// binaryPath is an absolute path for binary, which may be given relative to the install.
func binaryPath(where, binary string) string {
	if binary == "" {
		return ""
	}
	if strings.HasPrefix(binary, "/") {
		return binary
	}
	return trailingSlashes.ReplaceAllString(where, "") + "/" + leadingDotSlashes.ReplaceAllString(binary, "")
}

// substitute puts where the install ended up in place of every {}.
func substitute(command, where string) string {
	return strings.ReplaceAll(command, "{}", where)
}

// probeCommand answers presence, ownership and version in one round trip, never failing: absence
// is an answer.
func probeCommand(where, prefix, binary, healthCommand, versionCommand string) string {
	tests := []string{"test -d " + shellQuote(where)}
	if binary != "" {
		tests = append(tests, "test -x "+shellQuote(binary))
	}
	// both streams discarded: the question is whether it runs
	if healthCommand != "" {
		tests = append(tests, "{ "+substitute(healthCommand, where)+" ; } >/dev/null 2>&1")
	}
	version := ""
	if versionCommand != "" {
		version = "; " + substitute(versionCommand, where) + " 2>&1 || true"
	}
	return "{ " + strings.Join(tests, " && ") + " && echo present || true; }; " +
		// the prefix rather than where: stat follows a symlink, so asking through the link would
		// answer about whichever version it currently points at
		"echo '" + ownerMarker + "'; stat -c '%U:%G' " + shellQuote(prefix) + " 2>/dev/null || true; " +
		"echo '" + versionMarker + "'" + version
}

type probe struct {
	Installed             bool
	Owner, Group, Version string
}

// parseProbe splits what the probe said back into the things it answers.
func parseProbe(out, pattern string) probe {
	presence, rest, _ := strings.Cut(out, ownerMarker+"\n")
	ownership, printed, _ := strings.Cut(rest, versionMarker+"\n")
	owner, group, _ := strings.Cut(jscompat.Trim(ownership), ":")
	if i := strings.Index(group, ":"); i >= 0 {
		group = group[:i]
	}
	return probe{
		Installed: jscompat.Trim(presence) == "present",
		Owner:     owner,
		Group:     group,
		Version:   archive.ParseVersion(printed, pattern),
	}
}

// sourceChanged is whether the declared source changed. Version is not in it and cannot be.
func sourceChanged(old, wanted archiveSource) bool {
	return old != wanted
}

// installNeeded: absent is always yes; present is yes only when the program's own description of
// the source changed. With no previous description (a first apply) something already installed is
// left as it is.
func installNeeded(installed bool, old *archiveSource, wanted archiveSource) bool {
	if !installed {
		return true
	}
	if old == nil {
		return false
	}
	return sourceChanged(*old, wanted)
}

type archiveActKind string

const (
	actInstall archiveActKind = "install"
	actChown   archiveActKind = "chown"
	actNothing archiveActKind = "nothing"
)

// archiveAct is which of the three acts an install needs. Fetching replaces what is on the machine;
// a chown does not, so the chown is preferred wherever it is enough.
func archiveAct(actual probe, previous *archiveSource, wanted resolvedArchive) archiveActKind {
	if installNeeded(actual.Installed, previous, wanted.archiveSource) {
		return actInstall
	}
	if ownershipChanged(actual.Owner, actual.Group, wanted.Owner, wanted.Group) {
		return actChown
	}
	return actNothing
}

func orDefault(v *string, d string) string {
	if v == nil {
		return d
	}
	return *v
}

// resolveArchiveArgs settles the arguments, or refuses a url whose format it cannot tell.
func resolveArchiveArgs(args ArchiveArgs) (resolvedArchive, error) {
	format := archive.Format(orDefault(args.Format, ""))
	if args.Format == nil {
		format = archive.FormatOf(deref(args.URL))
	}
	if args.Format == nil && format == "" {
		return resolvedArchive{}, fmt.Errorf("cannot tell what kind of archive %s is from its name — give format: 'tar.gz' | 'tar.xz' | 'tar.bz2' | 'zip'", deref(args.URL))
	}
	link := orDefault(args.Link, "")
	where := whereOf(args.Prefix, link)
	return resolvedArchive{
		archiveSource: archiveSource{
			URL: deref(args.URL), Sha256: deref(args.Sha256), Prefix: args.Prefix, Link: link,
			Strip: deref(args.Strip), Format: format,
		},
		Name:           args.Name,
		Binary:         binaryPath(where, orDefault(args.Binary, "")),
		HealthCommand:  orDefault(args.HealthCommand, ""),
		Owner:          orDefault(args.Owner, archiveDefaultOwner),
		Group:          orDefault(args.Group, archiveDefaultGroup),
		VersionCommand: orDefault(args.VersionCommand, ""),
		VersionPattern: orDefault(args.VersionPattern, archive.DefaultVersionPattern),
		Where:          where,
	}, nil
}

func (r resolvedArchive) state(read probe) ArchiveState {
	format := string(r.Format)
	return ArchiveState{
		Name: r.Name, URL: &r.URL, Sha256: &r.Sha256, Prefix: r.Prefix, Link: &r.Link, Binary: &r.Binary,
		HealthCommand: &r.HealthCommand, Owner: read.Owner, Group: read.Group, Strip: &r.Strip, Format: &format,
		VersionCommand: r.VersionCommand, VersionPattern: &r.VersionPattern, Installed: read.Installed, Version: read.Version,
	}
}

func (s ArchiveState) source() archiveSource {
	return archiveSource{URL: deref(s.URL), Sha256: deref(s.Sha256), Prefix: s.Prefix, Link: deref(s.Link), Strip: deref(s.Strip), Format: archive.Format(deref(s.Format))}
}

// readArchive asks whether it is there and what it says it is. ask, not must: every way this can
// fail is an answer about the machine.
func readArchive(ctx context.Context, m *machine, where, prefix, binary, healthCommand, versionCommand, pattern string) (probe, error) {
	asked, err := m.ask(ctx, m.escalate(probeCommand(where, prefix, binary, healthCommand, versionCommand)))
	if err != nil {
		return probe{}, err
	}
	return parseProbe(asked.out, pattern), nil
}

func settleArchive(ctx context.Context, m *machine, args ArchiveArgs, previous *archiveSource) (ArchiveState, error) {
	wanted, err := resolveArchiveArgs(args)
	if err != nil {
		return ArchiveState{}, err
	}
	read := func() (probe, error) {
		return readArchive(ctx, m, wanted.Where, wanted.Prefix, wanted.Binary, wanted.HealthCommand, wanted.VersionCommand, wanted.VersionPattern)
	}
	before, err := read()
	if err != nil {
		return ArchiveState{}, err
	}
	act := archiveAct(before, previous, wanted)
	if act == actNothing {
		return wanted.state(before), nil
	}
	command := chownCommand(wanted.Prefix, wanted.Owner, wanted.Group)
	if act == actInstall {
		if command, err = archiveInstallScript(wanted.archiveSource, wanted.Owner, wanted.Group); err != nil {
			return ArchiveState{}, err
		}
	}
	if _, err := m.must(ctx, m.escalate(command)); err != nil {
		return ArchiveState{}, err
	}
	after, err := read()
	if err != nil {
		return ArchiveState{}, err
	}
	if !after.Installed {
		looked := wanted.Binary
		if looked == "" {
			looked = wanted.Where
		}
		return ArchiveState{}, fmt.Errorf("unpacked %s to %s on %s but %s is still not there — check strip, or whether the archive carries a top-level directory",
			wanted.URL, wanted.Prefix, m.describe(), looked)
	}
	if ownershipChanged(after.Owner, after.Group, wanted.Owner, wanted.Group) {
		return ArchiveState{}, fmt.Errorf("installed %s to %s on %s but it is owned by %s:%s rather than %s:%s — check that both exist on the machine",
			wanted.URL, wanted.Prefix, m.describe(), after.Owner, after.Group, wanted.Owner, wanted.Group)
	}
	return wanted.state(after), nil
}

// Check reports version patterns RE2 cannot compile.
func (Archive) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[ArchiveArgs], error) {
	args, failures, err := infer.DefaultCheck[ArchiveArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[ArchiveArgs]{Inputs: args, Failures: failures}, err
	}
	if v := req.NewInputs.Get("versionPattern"); v.IsString() {
		if refusal := archive.PatternRefusal(v.AsString()); refusal != "" {
			failures = append(failures, p.CheckFailure{Property: "versionPattern", Reason: refusal})
		}
	}
	return infer.CheckResponse[ArchiveArgs]{Inputs: args, Failures: failures}, nil
}

// Create installs it, or adopts what is already there: on a first apply an install that is present
// is left exactly as it is.
func (Archive) Create(ctx context.Context, req infer.CreateRequest[ArchiveArgs]) (infer.CreateResponse[ArchiveState], error) {
	if req.DryRun {
		wanted, err := resolveArchiveArgs(req.Inputs)
		if err != nil {
			return infer.CreateResponse[ArchiveState]{}, err
		}
		return infer.CreateResponse[ArchiveState]{ID: req.Inputs.Name, Output: wanted.state(probe{Installed: true, Owner: wanted.Owner, Group: wanted.Group})}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[ArchiveState]{}, err
	}
	state, err := settleArchive(ctx, m, req.Inputs, nil)
	if err != nil {
		return infer.CreateResponse[ArchiveState]{}, err
	}
	return infer.CreateResponse[ArchiveState]{ID: req.Inputs.Name, Output: state}, nil
}

// Read asks the machine; nothing there and Pulumi forgets it, so the next up installs it back.
// Installed, owner, group and version always come from the machine.
func (Archive) Read(ctx context.Context, req infer.ReadRequest[ArchiveArgs, ArchiveState]) (infer.ReadResponse[ArchiveArgs, ArchiveState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[ArchiveArgs, ArchiveState]{}, err
	}
	id, inputs, state := req.ID, req.Inputs, req.State
	if state.Prefix == "" && inputs.Prefix != "" {
		args := inputs
		args.Name = id
		wanted, err := resolveArchiveArgs(args)
		if err != nil {
			return infer.ReadResponse[ArchiveArgs, ArchiveState]{}, err
		}
		state = wanted.state(probe{})
	}
	if state.Prefix == "" {
		parts := strings.SplitN(id, "@", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || (len(parts) == 3 && parts[2] == "") {
			return infer.ReadResponse[ArchiveArgs, ArchiveState]{}, fmt.Errorf("archive import ID must be name@prefix or name@prefix@link")
		}
		id, inputs.Name, inputs.Prefix, state.Prefix = parts[0], parts[0], parts[1], parts[1]
		if len(parts) == 3 {
			inputs.Link, state.Link = &parts[2], &parts[2]
		}
	}
	if state.VersionPattern == nil {
		pattern := archive.DefaultVersionPattern
		state.VersionPattern = &pattern
	}
	if state.Format != nil && *state.Format == "" {
		format := string(archive.TarGz)
		state.Format = &format
	}
	link := deref(state.Link)
	actual, err := readArchive(ctx, m, whereOf(state.Prefix, link), state.Prefix, deref(state.Binary), deref(state.HealthCommand), state.VersionCommand, *state.VersionPattern)
	if err != nil || !actual.Installed {
		return infer.ReadResponse[ArchiveArgs, ArchiveState]{}, err
	}
	state.Name = id
	state.Installed, state.Owner, state.Group, state.Version = true, actual.Owner, actual.Group, actual.Version
	return infer.ReadResponse[ArchiveArgs, ArchiveState]{ID: id, Inputs: inputs, State: state}, nil
}

// Update settles against the previous source, so only a changed source refetches.
func (Archive) Update(ctx context.Context, req infer.UpdateRequest[ArchiveArgs, ArchiveState]) (infer.UpdateResponse[ArchiveState], error) {
	args := req.Inputs
	args.Name = req.ID
	if req.DryRun {
		wanted, err := resolveArchiveArgs(args)
		if err != nil {
			return infer.UpdateResponse[ArchiveState]{}, err
		}
		return infer.UpdateResponse[ArchiveState]{Output: wanted.state(probe{Installed: true, Owner: wanted.Owner, Group: wanted.Group, Version: req.State.Version})}, nil
	}
	// The first update after import records the declaration using the observations from Read.
	if req.State.URL == nil && req.State.Installed {
		wanted, err := resolveArchiveArgs(args)
		if err != nil {
			return infer.UpdateResponse[ArchiveState]{}, err
		}
		read := probe{Installed: true, Owner: req.State.Owner, Group: req.State.Group, Version: req.State.Version}
		return infer.UpdateResponse[ArchiveState]{Output: wanted.state(read)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[ArchiveState]{}, err
	}
	previous := req.State.source()
	state, err := settleArchive(ctx, m, args, &previous)
	if err != nil {
		return infer.UpdateResponse[ArchiveState]{}, err
	}
	return infer.UpdateResponse[ArchiveState]{Output: state}, nil
}

// Diff: gone is the drift this acts on, a version that moved on is not drift at all. A stricter
// read is a change; a wrong owner is a chown; a different prefix is a different install.
func (Archive) Diff(_ context.Context, req infer.DiffRequest[ArchiveArgs, ArchiveState]) (p.DiffResponse, error) {
	wanted, err := resolveArchiveArgs(req.Inputs)
	if err != nil {
		return p.DiffResponse{}, err
	}
	old := req.State
	resp := p.DiffResponse{DetailedDiff: map[string]p.PropertyDiff{}}
	changed := func(key string, yes bool) {
		if yes {
			resp.DetailedDiff[key] = p.PropertyDiff{Kind: p.Update}
		}
	}
	changed("installed", !old.Installed)
	changed("url", old.URL != nil && *old.URL != wanted.URL)
	changed("sha256", old.Sha256 != nil && *old.Sha256 != wanted.Sha256)
	changed("link", old.Link != nil && *old.Link != wanted.Link)
	changed("strip", old.Strip != nil && *old.Strip != wanted.Strip)
	changed("format", old.Format != nil && *old.Format != string(wanted.Format))
	changed("binary", old.Binary != nil && *old.Binary != wanted.Binary)
	changed("healthCommand", old.HealthCommand != nil && *old.HealthCommand != wanted.HealthCommand)
	changed("owner", ownershipChanged(old.Owner, old.Group, wanted.Owner, wanted.Group))
	if old.Prefix != wanted.Prefix {
		resp.DetailedDiff["prefix"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	// Record the declaration once so subsequent edits compare against real inputs.
	resp.HasChanges = old.URL == nil || len(resp.DetailedDiff) > 0
	return resp, nil
}

// archiveDeleteCommand removes the prefix, and the link only if it still points there: a
// replacement repoints it first, and deleting the old resource must not take the new one's link.
func archiveDeleteCommand(prefix, link string) string {
	unlink := ""
	if link != "" {
		unlink = `; [ "$(readlink ` + shellQuote(link) + ` 2>/dev/null)" = ` + shellQuote(prefix) + " ] " +
			"&& rm -f " + shellQuote(link) + " || true"
	}
	return "rm -rf " + shellQuote(prefix) + unlink
}

// Delete removes the unpacked tree.
func (Archive) Delete(ctx context.Context, req infer.DeleteRequest[ArchiveState]) (infer.DeleteResponse, error) {
	if req.State.Prefix == "" {
		return infer.DeleteResponse{}, fmt.Errorf("cannot delete %s: state carries no prefix to remove", req.ID)
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate(archiveDeleteCommand(req.State.Prefix, deref(req.State.Link))))
	return infer.DeleteResponse{}, err
}
