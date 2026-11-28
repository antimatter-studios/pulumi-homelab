package homelab

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/swap"
)

// Swap is whether the machine swaps, in both tenses: what is swapping now and what fstab or
// dphys-swapfile will bring back at the next boot. Ported from src/resources/swap.ts. Delete only
// unmasks the dphys unit; it never removes a swapfile or switches swap on or off.
type Swap struct{}

// SwapArgs are the inputs of a Swap.
type SwapArgs struct {
	Enabled   bool    `pulumi:"enabled"`
	SizeMb    *int    `pulumi:"sizeMb,optional"`
	Path      *string `pulumi:"path,optional"`
	Unit      *string `pulumi:"unit,optional"`
	Fstab     *string `pulumi:"fstab,optional"`
	OtherSwap *string `pulumi:"otherSwap,optional"`
}

// SwapActive is one line of /proc/swaps.
type SwapActive struct {
	Path     string  `pulumi:"path"`
	Kind     string  `pulumi:"kind"`
	SizeMb   int     `pulumi:"sizeMb"`
	Priority float64 `pulumi:"priority"`
}

// SwapState is what is kept in state.
type SwapState struct {
	Enabled   bool         `pulumi:"enabled"`
	SizeMb    *int         `pulumi:"sizeMb,optional"`
	Path      string       `pulumi:"path"`
	Unit      string       `pulumi:"unit"`
	FstabFile string       `pulumi:"fstabFile"`
	Active    []SwapActive `pulumi:"active"`
	Fstab     []string     `pulumi:"fstab"`
	Dphys     *string      `pulumi:"dphys,optional"`
	Zram      bool         `pulumi:"zram"`
	OtherSwap string       `pulumi:"otherSwap"`
}

func (a *SwapArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Enabled, "Whether the machine swaps to the declared file.")
	an.Describe(&a.SizeMb, "The swapfile's size in MB; required when enabled.")
	an.Describe(&a.Path, "The swapfile, /var/swap by default.")
	an.Describe(&a.Unit, "The swapfile service that is masked when swap is managed here, dphys-swapfile by default.")
	an.Describe(&a.Fstab, "The fstab, /etc/fstab by default.")
	an.Describe(&a.OtherSwap, "forbid (the default): off means no swap of any kind. allow: off means only the declared path never swaps, and other swap (zram, another disk) is left alone.")
}

func (s *SwapState) Annotate(an infer.Annotator) {
	an.Describe(&s.Active, "What /proc/swaps says is swapping now.")
	an.Describe(&s.Fstab, "The fstab swap lines that will bring swap back at the next boot.")
	an.Describe(&s.Dphys, "The swapfile unit's enablement, or absent when it is not installed.")
	an.Describe(&s.Zram, "Whether zram swap is configured.")
}

func (r *Swap) Annotate(a infer.Annotator) {
	a.Describe(&r, "Whether the machine swaps, now and after the next boot.")
	a.SetToken("index", "Swap")
}

const (
	swapDefaultPath = "/var/swap"
	swapDphys       = "dphys-swapfile"
)

type swapBefore struct {
	active []swap.Active
	fstab  []string
	dphys  *string
}

type swapWanted struct {
	enabled   bool
	sizeMb    *int
	path      string
	otherSwap string
}

// swapAction is whether anything needs doing: enable, disable or nothing.
func swapAction(before swapBefore, wanted swapWanted) string {
	counted := before
	if wanted.otherSwap == "allow" {
		counted = swapBefore{fstab: swap.FstabLinesFor(before.fstab, wanted.path), dphys: before.dphys}
		for _, s := range before.active {
			if s.Path == wanted.path {
				counted.active = append(counted.active, s)
			}
		}
	}
	alreadyOff := len(counted.active) == 0 && len(counted.fstab) == 0 && (before.dphys == nil || *before.dphys == "masked")
	atPath, atSize := false, false
	for _, s := range before.active {
		if s.Path == wanted.path {
			atPath = true
			if wanted.sizeMb != nil && s.SizeMB == *wanted.sizeMb {
				atSize = true
			}
		}
	}
	alreadyOn := atPath && len(before.fstab) > 0 && (wanted.sizeMb == nil || atSize)
	if wanted.enabled {
		if alreadyOn {
			return "nothing"
		}
		return "enable"
	}
	if alreadyOff {
		return "nothing"
	}
	return "disable"
}

var swapSection = regexp.MustCompile(`(?m)^#pulumi-homelab#\w+$`)

func swapReadCommand(unit, fstab string) string {
	return "cat /proc/swaps; echo '#pulumi-homelab#fstab'; " +
		"cat " + shellQuote(fstab) + " 2>/dev/null; echo '#pulumi-homelab#dphys'; " +
		"systemctl is-enabled " + shellQuote(unit) + " 2>/dev/null || true; echo '#pulumi-homelab#unit'; " +
		"systemctl list-unit-files " + shellQuote(unit+".service") + " --no-legend --no-pager 2>/dev/null || true; " +
		"echo '#pulumi-homelab#zram'; " +
		"{ test -e /dev/zram0 || test -f /etc/systemd/zram-generator.conf; } && echo yes || echo no"
}

func parseSwapRead(out, path string) SwapState {
	sections := append(swapSection.Split(out, -1), "", "", "", "", "")
	swaps, mounts, enabled, known, zram := sections[0], sections[1], sections[2], sections[3], sections[4]
	state := SwapState{Path: path, Active: []SwapActive{}, Fstab: swap.ParseFstabSwap(mounts), Zram: jscompat.Trim(zram) == "yes"}
	for _, a := range swap.ParseProcSwaps(swaps) {
		state.Active = append(state.Active, SwapActive{Path: a.Path, Kind: a.Kind, SizeMb: a.SizeMB, Priority: a.Priority})
	}
	if jscompat.Trim(known) != "" {
		d := jscompat.Trim(enabled)
		if d == "" {
			d = "unknown"
		}
		state.Dphys = &d
	}
	return state
}

func readSwap(ctx context.Context, m *machine, path, unit, fstab string) (SwapState, error) {
	asked, err := m.ask(ctx, m.escalate(swapReadCommand(unit, fstab)))
	if err != nil {
		return SwapState{}, err
	}
	if asked.code != 0 {
		detail := jscompat.Trim(asked.err)
		if detail == "" {
			detail = jscompat.Trim(asked.out)
		}
		return SwapState{}, fmt.Errorf("could not read swap state: %s", detail)
	}
	return parseSwapRead(asked.out, path), nil
}

func swapDisableCommand(fstab string, only *string) string {
	// ENVIRON preserves literal backslashes in the swap path.
	return "only=" + shellQuote(deref(only)) + " awk " +
		`'BEGIN{OFS=FS=" "; only=ENVIRON["only"]} /^[[:space:]]*#/ {print; next} $3 == "swap" && (only == "" || $1 == only) {print "#" $0; next} {print}' ` +
		shellQuote(fstab) + " > " + shellQuote(fstab+".pulumi") + " && mv " + shellQuote(fstab+".pulumi") + " " + shellQuote(fstab)
}

// list-unit-files with a filter exits 1 for no match. List all unit files
// successfully before deciding that the configured service is absent.
func swapUnitFileState(ctx context.Context, m *machine, unit string) (string, error) {
	result, err := m.commandResult(ctx, "systemctl list-unit-files", m.escalate("systemctl list-unit-files --no-legend --no-pager"))
	if err != nil {
		return "", err
	}
	name := unit
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	for _, line := range strings.Split(result.out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == name {
			return fields[1], nil
		}
	}
	return "", nil
}

func disableSwap(ctx context.Context, m *machine, unit, fstab string, only *string) error {
	state, err := swapUnitFileState(ctx, m, unit)
	if err != nil {
		return err
	}
	active, err := m.commandResult(ctx, "cat /proc/swaps", m.escalate("cat /proc/swaps"))
	if err != nil {
		return err
	}
	swapping := slices.ContainsFunc(swap.ParseProcSwaps(active.out), func(s swap.Active) bool {
		return only == nil || s.Path == *only
	})
	if swapping {
		command := "swapoff -a"
		if only != nil {
			command = "swapoff -- " + shellQuote(*only)
		}
		if _, err := m.commandResult(ctx, "swapoff", m.escalate(command)); err != nil {
			return err
		}
	}
	if _, err := m.commandResult(ctx, "awk / mv (fstab)", m.escalate(swapDisableCommand(fstab, only))); err != nil {
		return err
	}

	switch state {
	case "":
		return nil
	case "masked", "masked-runtime":
		_, err := m.commandResult(ctx, "systemctl stop", m.escalate("systemctl stop -- "+shellQuote(unit)))
		if err != nil || state == "masked" {
			return err
		}
	default:
		if _, err := m.commandResult(ctx, "systemctl disable --now", m.escalate("systemctl disable --now -- "+shellQuote(unit))); err != nil {
			return err
		}
	}
	_, err = m.commandResult(ctx, "systemctl mask", m.escalate("systemctl mask -- "+shellQuote(unit)))
	return err
}

func swapEnableCommand(path string, sizeMb int, active bool, dphys *string, unit, fstab string) string {
	file, directory := shellQuote(path), shellQuote(parentOf(path))
	size := strconv.Itoa(sizeMb)
	steps := []string{
		"filesystem=$(stat -f -c %T " + directory + ")",
		`if [ "$filesystem" = btrfs ]; then echo "a swapfile on btrfs needs chattr +C on an empty file, which this resource does not do" >&2; exit 1; fi`,
	}
	if active {
		steps = append(steps, "swapoff "+file)
	}
	steps = append(steps,
		"{ fallocate -l "+size+"M "+file+" || dd if=/dev/zero of="+file+" bs=1M count="+size+" status=none; }",
		"chmod 0600 "+file,
		"mkswap "+file+" >/dev/null",
		"swapon "+file,
		"if grep -qF -- "+file+" "+shellQuote(fstab)+"; then :; "+
			`else status=$?; [ "$status" -eq 1 ] || exit "$status"; printf '%s none swap sw 0 0\n' `+file+" >> "+shellQuote(fstab)+"; fi",
	)
	if dphys != nil {
		if *dphys == "masked" || *dphys == "masked-runtime" {
			steps = append(steps, "systemctl stop "+shellQuote(unit))
		} else {
			steps = append(steps, "systemctl disable --now "+shellQuote(unit))
		}
		steps = append(steps, "systemctl mask "+shellQuote(unit))
	}
	return strings.Join(steps, " && ")
}

func swapSettle(ctx context.Context, m *machine, a SwapArgs) (SwapState, error) {
	path, unit, fstab, otherSwap := orDefault(a.Path, swapDefaultPath), orDefault(a.Unit, swapDphys), orDefault(a.Fstab, fstabPath), orDefault(a.OtherSwap, "forbid")
	before, err := readSwap(ctx, m, path, unit, fstab)
	if err != nil {
		return SwapState{}, err
	}
	b := swapBefore{fstab: before.Fstab, dphys: before.Dphys}
	for _, s := range before.Active {
		b.active = append(b.active, swap.Active{Path: s.Path, Kind: s.Kind, SizeMB: s.SizeMb, Priority: s.Priority})
	}
	switch swapAction(b, swapWanted{enabled: a.Enabled, sizeMb: a.SizeMb, path: path, otherSwap: otherSwap}) {
	case "enable":
		if a.SizeMb == nil {
			return SwapState{}, errors.New("swap that is enabled needs a sizeMb")
		}
		active := slices.ContainsFunc(before.Active, func(s SwapActive) bool { return s.Path == path })
		if _, err := m.must(ctx, m.escalate(swapEnableCommand(path, *a.SizeMb, active, before.Dphys, unit, fstab))); err != nil {
			return SwapState{}, err
		}
	case "disable":
		var only *string
		if otherSwap == "allow" {
			only = &path
		}
		if err := disableSwap(ctx, m, unit, fstab, only); err != nil {
			return SwapState{}, err
		}
	}
	after, err := readSwap(ctx, m, path, unit, fstab)
	if err != nil {
		return SwapState{}, err
	}
	after.Unit, after.FstabFile, after.Enabled, after.SizeMb, after.OtherSwap = unit, fstab, a.Enabled, a.SizeMb, otherSwap
	return after, nil
}

// Create settles swap; the id is the path.
func (Swap) Create(ctx context.Context, req infer.CreateRequest[SwapArgs]) (infer.CreateResponse[SwapState], error) {
	a := req.Inputs
	path := orDefault(a.Path, swapDefaultPath)
	if req.DryRun {
		return infer.CreateResponse[SwapState]{ID: path, Output: SwapState{Enabled: a.Enabled, SizeMb: a.SizeMb, Path: path, Unit: orDefault(a.Unit, swapDphys),
			FstabFile: orDefault(a.Fstab, fstabPath), Active: []SwapActive{}, Fstab: []string{}, OtherSwap: orDefault(a.OtherSwap, "forbid")}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SwapState]{}, err
	}
	state, err := swapSettle(ctx, m, a)
	if err != nil {
		return infer.CreateResponse[SwapState]{}, err
	}
	return infer.CreateResponse[SwapState]{ID: path, Output: state}, nil
}

// Read reports swap in both tenses. There is always an answer, so it is never forgotten.
func (Swap) Read(ctx context.Context, req infer.ReadRequest[SwapArgs, SwapState]) (infer.ReadResponse[SwapArgs, SwapState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SwapArgs, SwapState]{}, err
	}
	old := req.State
	imported := old.Path == ""
	if imported {
		old.Unit, old.FstabFile, old.OtherSwap = swapDphys, fstabPath, "forbid"
	}
	actual, err := readSwap(ctx, m, req.ID, old.Unit, old.FstabFile)
	if err != nil {
		return infer.ReadResponse[SwapArgs, SwapState]{}, err
	}
	actual.Enabled, actual.SizeMb, actual.Unit, actual.FstabFile, actual.OtherSwap = old.Enabled, old.SizeMb, old.Unit, old.FstabFile, old.OtherSwap
	if actual.SizeMb == nil && len(actual.Active) > 0 {
		size := actual.Active[0].SizeMb
		actual.SizeMb = &size
	}
	inputs := req.Inputs
	if imported {
		// as the TypeScript read: on when anything swaps now or will at boot, at the first size seen
		actual.Enabled = len(actual.Active) > 0 || len(actual.Fstab) > 0
		inputs = SwapArgs{Enabled: actual.Enabled, SizeMb: actual.SizeMb, Path: &actual.Path, Unit: &actual.Unit, Fstab: &actual.FstabFile, OtherSwap: &actual.OtherSwap}
	}
	return infer.ReadResponse[SwapArgs, SwapState]{ID: req.ID, Inputs: inputs, State: actual}, nil
}

// Update settles swap again at the path in the id unless another is given.
func (Swap) Update(ctx context.Context, req infer.UpdateRequest[SwapArgs, SwapState]) (infer.UpdateResponse[SwapState], error) {
	a := req.Inputs
	if a.Path == nil {
		id := req.ID
		a.Path = &id
	}
	if req.DryRun {
		s := req.State
		s.Enabled, s.SizeMb, s.OtherSwap = a.Enabled, a.SizeMb, orDefault(a.OtherSwap, "forbid")
		return infer.UpdateResponse[SwapState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SwapState]{}, err
	}
	state, err := swapSettle(ctx, m, a)
	return infer.UpdateResponse[SwapState]{Output: state}, err
}

// Diff is whether the machine already is as declared, in both tenses; another path replaces.
func (Swap) Diff(_ context.Context, req infer.DiffRequest[SwapArgs, SwapState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	path, otherSwap := orDefault(a.Path, swapDefaultPath), orDefault(a.OtherSwap, "forbid")
	active, fstab := old.Active, old.Fstab
	if otherSwap == "allow" {
		active = nil
		for _, s := range old.Active {
			if s.Path == path {
				active = append(active, s)
			}
		}
		fstab = swap.FstabLinesFor(old.Fstab, path)
	}
	activeNow := len(active) > 0
	comingBack := len(fstab) > 0 || (old.Dphys != nil && *old.Dphys == "enabled")
	atPath := false
	for _, s := range old.Active {
		atPath = atPath || s.Path == path
	}
	satisfied := !activeNow && !comingBack
	if a.Enabled {
		satisfied = activeNow && atPath
	}
	sizeChanged := a.Enabled && a.SizeMb != nil && (old.SizeMb == nil || *old.SizeMb != *a.SizeMb)
	changed := !satisfied || old.Enabled != a.Enabled || old.Path != path || old.OtherSwap != otherSwap || sizeChanged
	var replaces []string
	if old.Path != path {
		replaces = []string{"path"}
	}
	return diffOf(changed, false, replaces...), nil
}

// Delete only unmasks the swapfile unit; the swapfile and the machine's swap stay as they are.
func (Swap) Delete(ctx context.Context, req infer.DeleteRequest[SwapState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	unit := req.State.Unit
	if unit == "" {
		unit = swapDphys
	}
	state, err := swapUnitFileState(ctx, m, unit)
	if err != nil || (state != "masked" && state != "masked-runtime") {
		return infer.DeleteResponse{}, err
	}
	if req.State.Dphys == nil || *req.State.Dphys != state {
		warnRetained(ctx, unit, "its current mask does not match the recorded state")
		return infer.DeleteResponse{}, nil
	}
	command := "systemctl unmask"
	if state == "masked-runtime" {
		command += " --runtime"
	}
	_, err = m.commandResult(ctx, "systemctl unmask", m.escalate(command+" -- "+shellQuote(unit)))
	return infer.DeleteResponse{}, err
}
