package homelab

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/swappart"
)

// SwapPartition is a swap partition, found by its GPT name: created in free space if no partition
// has that name, given a swap signature only when it has none at all, swapping at a priority, and
// brought back at boot by a systemd swap unit of its own. It never moves, resizes or deletes another
// partition, and never formats one that holds anything. Delete turns the swap off and removes the
// unit, and keeps the partition unless deletePartition says otherwise.
//
// The boot side is a unit per partition, /etc/systemd/system/<escaped by-partlabel path>.swap,
// rather than an fstab line: two partitions are two files, so resources settling side by side never
// edit one shared file, and no fstab line is keyed by a mount point that is `none` for every swap.
// Declare the machine's Swap resource with otherSwap 'allow', which leaves this swap alone.
type SwapPartition struct{}

// SwapPartitionArgs are the inputs of a SwapPartition.
type SwapPartitionArgs struct {
	Disk            string  `pulumi:"disk"`
	Name            string  `pulumi:"name"`
	SizeGib         int     `pulumi:"sizeGib"`
	Start           *int    `pulumi:"start,optional"`
	Priority        int     `pulumi:"priority"`
	Discard         *string `pulumi:"discard,optional"`
	Persistent      *bool   `pulumi:"persistent,optional"`
	DeletePartition *bool   `pulumi:"deletePartition,optional"`
}

// SwapPartitionState is what is kept in state: the declaration, and what the machine has.
type SwapPartitionState struct {
	Disk            string  `pulumi:"disk"`
	Name            string  `pulumi:"name"`
	SizeGib         int     `pulumi:"sizeGib"`
	Start           *int    `pulumi:"start,optional"`
	Priority        int     `pulumi:"priority"`
	Discard         *string `pulumi:"discard,optional"`
	Persistent      bool    `pulumi:"persistent"`
	DeletePartition bool    `pulumi:"deletePartition"`

	DiskDevice     string  `pulumi:"diskDevice"`
	Device         string  `pulumi:"device"`
	Number         int     `pulumi:"number"`
	StartSector    int     `pulumi:"startSector"`
	SizeBytes      int     `pulumi:"sizeBytes"`
	PartUuid       string  `pulumi:"partUuid"`
	PartType       string  `pulumi:"partType"`
	Signature      string  `pulumi:"signature"`
	Label          string  `pulumi:"label"`
	Active         bool    `pulumi:"active"`
	ActivePriority *int    `pulumi:"activePriority,optional"`
	Unit           string  `pulumi:"unit"`
	UnitFile       *string `pulumi:"unitFile,optional"`
}

func (a *SwapPartitionArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Disk, "The disk the partition is made on, e.g. /dev/nvme0n1. Only used to create it: the partition is found by its name wherever it is, so disks enumerated in another order are not a change.")
	an.Describe(&a.Name, "The GPT partition name, which is also the swap label and the id (import with the name, or <disk>:<name>). 1-16 characters of A-Z, a-z, 0-9, '.', '_' and '-'.")
	an.Describe(&a.SizeGib, "The partition's size in GiB. A different size replaces the resource; an existing partition is never resized.")
	an.Describe(&a.Start, "The first sector of a new partition. By default the first 1 MiB boundary after the last partition. Either way only free space is used.")
	an.Describe(&a.Priority, "The swap priority, 0-32767. Higher is used first.")
	an.Describe(&a.Discard, "once, pages or both: what swapon's --discard does. Applied when the swap is next switched on (a reboot, or a priority change); the kernel does not report it, so it is compared with what was declared.")
	an.Describe(&a.Persistent, "Whether the swap comes back at boot, through a systemd swap unit this resource writes. On by default. Off writes no unit (and removes this resource's own) while the swap stays on now: for adopting a partition something else still enables at boot.")
	an.Describe(&a.DeletePartition, "Whether delete also removes the partition from the partition table. Off by default: delete turns the swap off and removes the unit, and the partition and its data stay.")
}

func (s *SwapPartitionState) Annotate(an infer.Annotator) {
	an.Describe(&s.DiskDevice, "The disk the partition is on, as the kernel names it.")
	an.Describe(&s.Device, "The partition's device node.")
	an.Describe(&s.Number, "The partition number.")
	an.Describe(&s.StartSector, "The partition's first sector.")
	an.Describe(&s.SizeBytes, "The partition's size in bytes.")
	an.Describe(&s.PartUuid, "The partition's GPT unique id.")
	an.Describe(&s.PartType, "The partition's GPT type GUID.")
	an.Describe(&s.Signature, "What blkid finds on the partition: swap, empty for nothing, or the other thing it holds.")
	an.Describe(&s.Label, "The swap signature's label.")
	an.Describe(&s.Active, "Whether the kernel is swapping to it now.")
	an.Describe(&s.ActivePriority, "The priority the kernel is using it at.")
	an.Describe(&s.Unit, "The systemd swap unit's name.")
	an.Describe(&s.UnitFile, "The swap unit file as the machine has it, absent when there is none.")
}

func (r *SwapPartition) Annotate(a infer.Annotator) {
	a.Describe(&r, "A swap partition found by its GPT name: created in free space only, formatted only when blank, swapping at a priority and enabled at boot by a systemd swap unit. Delete keeps the partition unless told otherwise.")
	a.SetToken("index", "SwapPartition")
}

const swapPartUnits = "/etc/systemd/system"

func swapPartUnitPath(name string) string {
	return swapPartUnits + "/" + swappart.UnitName(swappart.What(name))
}

func validSwapPartition(a SwapPartitionArgs) error {
	if err := swappart.ValidName(a.Name); err != nil {
		return err
	}
	if !strings.HasPrefix(a.Disk, "/") {
		return fmt.Errorf("disk %q must be an absolute path", a.Disk)
	}
	if a.SizeGib < 1 {
		return errors.New("sizeGib must be at least 1")
	}
	if a.Start != nil && *a.Start < 0 {
		return errors.New("start must be a sector number")
	}
	if err := swappart.ValidPriority(a.Priority); err != nil {
		return err
	}
	return swappart.ValidDiscard(a.Discard)
}

// declared is the state's declaration half, from the inputs.
func swapPartDeclared(a SwapPartitionArgs) SwapPartitionState {
	persistent := true
	if a.Persistent != nil {
		persistent = *a.Persistent
	}
	return SwapPartitionState{Disk: a.Disk, Name: a.Name, SizeGib: a.SizeGib, Start: a.Start, Priority: a.Priority, Discard: a.Discard,
		Persistent: persistent, DeletePartition: deref(a.DeletePartition), Unit: swappart.UnitName(swappart.What(a.Name))}
}

// swapPartScanCommand lists every disk's partition table, the declared disk first. Each disk is
// named by its resolved path, so a by-id path and the kernel's name are one disk. Read-only.
func swapPartScanCommand(disk string) string {
	each := `r=$(readlink -f "$d"); printf '` + swappart.DiskMarker + `%s\n' "$r"; ` +
		`printf '` + swappart.HashMarker + `%s\n' "$(sfdisk --dump "$r" 2>/dev/null | sha256sum | cut -d' ' -f1)"; ` +
		`sfdisk --json "$r" 2>/dev/null; echo`
	disks := `$(lsblk -dnpo PATH,TYPE 2>/dev/null | awk '$2 == "disk" {print $1}')`
	if disk != "" {
		disks = shellQuote(disk) + " " + disks
	}
	return "for d in " + disks + "; do " + each + "; done; true"
}

const (
	swapPartBlkid = "#pulumi-homelab#blkid "
	swapPartSwaps = "#pulumi-homelab#swaps"
	swapPartUnit  = "#pulumi-homelab#unit"
	swapPartFound = "#pulumi-homelab#unitfound"
	swapPartMem   = "#pulumi-homelab#mem"
)

// swapPartDetailCommand reads what is on one partition, the swap areas in use, the unit file and
// the memory available. Read-only.
func swapPartDetailCommand(node, unitPath string) string {
	n := shellQuote(node)
	return "if [ -e " + n + " ]; then blkid -p -o export " + n + "; echo '" + swapPartBlkid + "'$?; else echo '" + swapPartBlkid + "missing'; fi; " +
		"echo '" + swapPartSwaps + "'; swapon --show=NAME,TYPE,SIZE,USED,PRIO --raw --bytes --noheadings; " +
		"echo '" + swapPartUnit + "'; cat " + shellQuote(unitPath) + " 2>/dev/null && echo '" + swapPartFound + "'; " +
		"echo '" + swapPartMem + "'; awk '/^MemAvailable:/ {print $2}' /proc/meminfo"
}

// swapPartDetail is what swapPartDetailCommand found.
type swapPartDetail struct {
	signature    swappart.Signature
	missing      bool
	active       *swappart.Active
	unitFile     *string
	memAvailable int64
}

func parseSwapPartDetail(out, node string) (swapPartDetail, error) {
	var d swapPartDetail
	blkid, rest, _ := strings.Cut(out, "\n"+swapPartSwaps+"\n")
	if strings.HasPrefix(blkid, swapPartBlkid) {
		blkid = "\n" + blkid
	}
	probe, codeText, _ := strings.Cut(blkid, "\n"+swapPartBlkid)
	codeText = strings.TrimSpace(codeText)
	if codeText == "missing" {
		d.missing = true
	} else {
		code, err := strconv.Atoi(codeText)
		if err != nil {
			return d, fmt.Errorf("could not read blkid's exit code from %q", codeText)
		}
		if d.signature, err = swappart.ParseBlkid(probe, code); err != nil {
			return d, fmt.Errorf("%s: %w", node, err)
		}
	}
	swaps, rest, _ := strings.Cut(rest, swapPartUnit+"\n")
	if a, ok := swappart.LookupActive(swappart.ParseSwaps(swaps), node); ok {
		d.active = &a
	}
	unit, mem, _ := strings.Cut(rest, swapPartMem+"\n")
	if content, ok := strings.CutSuffix(unit, swapPartFound+"\n"); ok {
		d.unitFile = &content
	}
	if kb, err := strconv.ParseInt(strings.TrimSpace(mem), 10, 64); err == nil {
		d.memAvailable = kb << 10
	}
	return d, nil
}

// swapPartObserved is a partition as found, with what is on it.
type swapPartObserved struct {
	found  swappart.Found
	detail swapPartDetail
}

func (o swapPartObserved) node() string { return o.found.Partition.Node }

// locateSwapPart finds the partition by name, preferring the declared disk. ok is false when no
// disk has it; tables is every disk's partition table, the declared disk's first.
func locateSwapPart(ctx context.Context, m *machine, disk, name string) (obs swapPartObserved, tables []swappart.Table, ok bool, err error) {
	out, err := m.must(ctx, m.escalate(swapPartScanCommand(disk)))
	if err != nil {
		return obs, nil, false, err
	}
	if tables, err = swappart.ParseScan(out); err != nil {
		return obs, nil, false, err
	}
	prefer := ""
	if disk != "" && len(tables) > 0 {
		prefer = tables[0].Path
	}
	found, ok, err := swappart.Find(tables, name, prefer)
	if err != nil || !ok {
		return obs, tables, ok, err
	}
	obs.found = found
	detail, err := m.must(ctx, m.escalate(swapPartDetailCommand(found.Partition.Node, swapPartUnitPath(name))))
	if err != nil {
		return obs, tables, false, err
	}
	obs.detail, err = parseSwapPartDetail(detail, found.Partition.Node)
	return obs, tables, true, err
}

// observe puts what was found into a state that already has its declaration.
func (o swapPartObserved) into(s SwapPartitionState) SwapPartitionState {
	part, table := o.found.Partition, o.found.Table
	s.DiskDevice, s.Device, s.Number, s.StartSector = table.Path, part.Node, part.Number, int(part.Start)
	s.SizeBytes, s.PartUuid, s.PartType = int(part.Size*table.SectorSize), part.UUID, part.Type
	s.Signature, s.Label = o.detail.signature.Kind, o.detail.signature.Label
	s.Active, s.ActivePriority = o.detail.active != nil, nil
	if o.detail.active != nil {
		prio := o.detail.active.Priority
		s.ActivePriority = &prio
	}
	s.Unit, s.UnitFile = swappart.UnitName(swappart.What(s.Name)), o.detail.unitFile
	return s
}

// checkGeometry refuses a partition that has the name but not the declared shape: this resource
// never resizes or moves one, and never takes over a partition of another type.
func checkSwapPartGeometry(o swapPartObserved, a SwapPartitionArgs, diskPath string) error {
	part, table := o.found.Partition, o.found.Table
	if diskPath != "" && table.Path != diskPath {
		return fmt.Errorf("a partition named %s already exists on %s (%s), not on %s; SwapPartition never moves one", a.Name, table.Path, part.Node, a.Disk)
	}
	if size := part.Size * table.SectorSize; size != int64(a.SizeGib)<<30 {
		return fmt.Errorf("%s (%s) is %d bytes, not %d GiB; SwapPartition never resizes a partition", part.Node, a.Name, size, a.SizeGib)
	}
	if a.Start != nil && part.Start != int64(*a.Start) {
		return fmt.Errorf("%s (%s) starts at sector %d, not %d; SwapPartition never moves a partition", part.Node, a.Name, part.Start, *a.Start)
	}
	if part.Type != swappart.SwapType {
		return fmt.Errorf("%s (%s) has GPT type %s, not Linux swap (%s); it is left alone", part.Node, a.Name, part.Type, swappart.SwapType)
	}
	return nil
}

// createSwapPartCommand adds the partition with sgdisk and tells the kernel about that partition
// alone. It holds the disk's lock (as udev and other partitioning tools expect) and first checks the
// table is the one the placement was planned against, so two resources on one disk cannot both
// claim the same free space.
func createSwapPartCommand(t swappart.Table, place swappart.Placement, name string) string {
	disk, n := shellQuote(t.Path), strconv.Itoa(place.Number)
	node := shellQuote(swappart.NodeOf(t.Path, place.Number))
	inner := `test "$(sfdisk --dump ` + disk + ` 2>/dev/null | sha256sum | cut -d' ' -f1)" = ` + shellQuote(t.Hash) +
		` || { echo "the partition table of "` + disk + `" changed since it was read; run again" >&2; exit 1; }; ` +
		"sgdisk -n " + n + ":" + strconv.FormatInt(place.Start, 10) + ":" + strconv.FormatInt(place.End, 10) +
		" -t " + n + ":8200 -c " + n + ":" + shellQuote(name) + " " + disk + " >/dev/null"
	return "flock " + disk + " sh -c " + shellQuote(inner) + " && " +
		"{ partx -a --nr " + n + " " + disk + " 2>/dev/null || partx -u --nr " + n + " " + disk + "; } && " +
		"{ command -v udevadm >/dev/null && udevadm settle || true; } && " +
		"for i in 1 2 3 4 5 6 7 8 9 10; do [ -e " + node + " ] && break; sleep 1; done; [ -e " + node + " ]"
}

// mkswapCommand gives a partition its swap signature, checking once more on the machine that there
// is no signature at all.
func mkswapCommand(node, name string) string {
	n := shellQuote(node)
	return "blkid -p " + n + " >/dev/null 2>&1; rc=$?; " +
		"if [ $rc -ne 2 ]; then echo " + n + "' is not blank (blkid exit '$rc'); not formatting it' >&2; exit 1; fi; " +
		"mkswap -L " + shellQuote(name) + " " + n + " >/dev/null"
}

func signatureRefusal(node, name, kind string) error {
	return fmt.Errorf("%s (%s) holds a %s signature. SwapPartition formats only a partition with no signature at all; "+
		"if what is on it is yours to lose, clear it by hand (wipefs -a %s) and run again", node, name, kind, node)
}

// settleSwapPart makes the machine match the declaration: the partition (created only when
// mayCreate), its signature, the unit and the swap.
func settleSwapPart(ctx context.Context, m *machine, a SwapPartitionArgs, mayCreate bool) (SwapPartitionState, error) {
	wanted := swapPartDeclared(a)
	obs, tables, ok, err := locateSwapPart(ctx, m, a.Disk, a.Name)
	if err != nil {
		return wanted, err
	}
	diskPath := ""
	if len(tables) > 0 {
		diskPath = tables[0].Path
	}
	if !ok {
		if !mayCreate {
			return wanted, fmt.Errorf("no partition is named %s any more; refresh, and the next deployment creates it", a.Name)
		}
		if len(tables) == 0 {
			return wanted, fmt.Errorf("%s could not be read", a.Disk)
		}
		var start *int64
		if a.Start != nil {
			s := int64(*a.Start)
			start = &s
		}
		place, err := swappart.Plan(tables[0], a.SizeGib, start)
		if err != nil {
			return wanted, err
		}
		if _, err := m.must(ctx, m.escalate(createSwapPartCommand(tables[0], place, a.Name))); err != nil {
			return wanted, err
		}
		if obs, _, ok, err = locateSwapPart(ctx, m, a.Disk, a.Name); err != nil {
			return wanted, err
		} else if !ok {
			return wanted, fmt.Errorf("sgdisk made partition %d on %s, but it is not named %s on a read back", place.Number, diskPath, a.Name)
		}
	}
	if !mayCreate {
		// once made, the name is the partition's identity, wherever the kernel enumerates its disk
		diskPath = ""
	}
	if err := checkSwapPartGeometry(obs, a, diskPath); err != nil {
		return wanted, err
	}
	node := obs.node()
	if obs.detail.missing {
		return wanted, fmt.Errorf("%s is in the partition table but the kernel has no device for it; run partx -a --nr %d %s", node, obs.found.Partition.Number, obs.found.Table.Path)
	}
	switch obs.detail.signature.Kind {
	case "swap":
	case "":
		if _, err := m.must(ctx, m.escalate(mkswapCommand(node, a.Name))); err != nil {
			return wanted, err
		}
	default:
		return wanted, signatureRefusal(node, a.Name, obs.detail.signature.Kind)
	}

	unitPath, unit := swapPartUnitPath(a.Name), shellQuote(wanted.Unit)
	content := swappart.Unit(a.Name, a.Priority, a.Discard)
	var steps []string
	switch {
	case wanted.Persistent && (obs.detail.unitFile == nil || *obs.detail.unitFile != content):
		steps = append(steps, heredoc(unitPath, content, "chmod 0644 "+shellQuote(unitPath), "systemctl daemon-reload", "systemctl enable "+unit+" 2>&1"))
	case wanted.Persistent:
		steps = append(steps, "systemctl is-enabled --quiet "+unit+" || systemctl enable "+unit+" 2>&1")
	case obs.detail.unitFile != nil:
		// off at boot, on now: the unit goes, the swap stays
		steps = append(steps, "systemctl disable "+unit+" 2>&1; rm -f "+shellQuote(unitPath)+" && systemctl daemon-reload")
	}
	on := "swapon " + swappart.SwaponArgs(a.Priority, a.Discard) + " " + shellQuote(node)
	if wanted.Persistent {
		on = "systemctl start " + unit
	}
	switch active := obs.detail.active; {
	case active == nil:
		steps = append(steps, on)
	case active.Priority != a.Priority:
		// the kernel cannot change a priority in place: off and on again, if the pages fit in memory
		if err := swappart.SafeToSwapoff(node, active.Used, obs.detail.memAvailable); err != nil {
			return wanted, err
		}
		if wanted.Persistent {
			steps = append(steps, "systemctl restart "+unit)
		} else {
			steps = append(steps, "swapoff "+shellQuote(node)+" && "+on)
		}
	}
	for _, step := range steps {
		if _, err := m.must(ctx, m.escalate(step)); err != nil {
			return wanted, err
		}
	}
	obs, _, ok, err = locateSwapPart(ctx, m, a.Disk, a.Name)
	if err != nil {
		return wanted, err
	}
	if !ok {
		return wanted, fmt.Errorf("%s is gone after it was set up", a.Name)
	}
	state := obs.into(wanted)
	if !state.Active {
		return state, fmt.Errorf("%s (%s) is set up but not swapping; see systemctl status %s", node, a.Name, wanted.Unit)
	}
	return state, nil
}

// Create finds or makes the partition and has it swapping; the id is the name.
func (SwapPartition) Create(ctx context.Context, req infer.CreateRequest[SwapPartitionArgs]) (infer.CreateResponse[SwapPartitionState], error) {
	a := req.Inputs
	if err := validSwapPartition(a); err != nil {
		return infer.CreateResponse[SwapPartitionState]{}, err
	}
	if req.DryRun {
		return infer.CreateResponse[SwapPartitionState]{ID: a.Name, Output: swapPartDeclared(a)}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SwapPartitionState]{}, err
	}
	state, err := settleSwapPart(ctx, m, a, true)
	if err != nil {
		return infer.CreateResponse[SwapPartitionState]{}, err
	}
	return infer.CreateResponse[SwapPartitionState]{ID: a.Name, Output: state}, nil
}

// Read reports the partition as it is. A partition no disk has by that name is forgotten. An
// import (an id alone: the name, or <disk>:<name>) reads the declaration back from the machine:
// the disk it is on (as the id names it, or the kernel's name), its size, the priority it swaps at,
// and the unit's settings if it has one.
func (SwapPartition) Read(ctx context.Context, req infer.ReadRequest[SwapPartitionArgs, SwapPartitionState]) (infer.ReadResponse[SwapPartitionArgs, SwapPartitionState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SwapPartitionArgs, SwapPartitionState]{}, err
	}
	old, id := req.State, req.ID
	imported := old.Name == ""
	if imported {
		// an import id is the name, or <disk>:<name> to name the disk as the program does (a by-id
		// path, or a disk lsblk does not list); the id kept is the name either way
		old.Name = id
		if at := strings.LastIndex(id, ":"); at > 0 {
			old.Disk, old.Name = id[:at], id[at+1:]
		}
		id = old.Name
	}
	obs, _, ok, err := locateSwapPart(ctx, m, old.Disk, old.Name)
	if err != nil || !ok {
		return infer.ReadResponse[SwapPartitionArgs, SwapPartitionState]{}, err
	}
	state := obs.into(old)
	inputs := req.Inputs
	if imported {
		inputs = swapPartImported(state)
		if old.Disk != "" {
			inputs.Disk = old.Disk
		}
		state.Disk, state.SizeGib, state.Priority, state.Discard, state.Persistent = inputs.Disk, inputs.SizeGib, inputs.Priority, inputs.Discard, *inputs.Persistent
	}
	return infer.ReadResponse[SwapPartitionArgs, SwapPartitionState]{ID: id, Inputs: inputs, State: state}, nil
}

// swapPartImported is the declaration an import reads back: what a program has to say for the
// import to show no change.
func swapPartImported(s SwapPartitionState) SwapPartitionArgs {
	in := SwapPartitionArgs{Disk: s.DiskDevice, Name: s.Name, SizeGib: s.SizeBytes >> 30}
	persistent := s.UnitFile != nil
	in.Persistent = &persistent
	var unitPriority *int
	if s.UnitFile != nil {
		unitPriority, in.Discard = swappart.UnitSettings(*s.UnitFile)
	}
	switch {
	case s.ActivePriority != nil:
		in.Priority = *s.ActivePriority
	case unitPriority != nil:
		in.Priority = *unitPriority
	}
	return in
}

// Update settles the partition again; it never creates, moves or resizes one.
func (SwapPartition) Update(ctx context.Context, req infer.UpdateRequest[SwapPartitionArgs, SwapPartitionState]) (infer.UpdateResponse[SwapPartitionState], error) {
	a := req.Inputs
	if err := validSwapPartition(a); err != nil {
		return infer.UpdateResponse[SwapPartitionState]{}, err
	}
	if req.DryRun {
		s := req.State
		d := swapPartDeclared(a)
		s.Disk, s.SizeGib, s.Start, s.Priority, s.Discard, s.Persistent, s.DeletePartition = d.Disk, d.SizeGib, d.Start, d.Priority, d.Discard, d.Persistent, d.DeletePartition
		return infer.UpdateResponse[SwapPartitionState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SwapPartitionState]{}, err
	}
	state, err := settleSwapPart(ctx, m, a, false)
	return infer.UpdateResponse[SwapPartitionState]{Output: state}, err
}

// Diff compares the declaration with what was last seen. Another name replaces, keeping the old
// partition. Another disk, size or start would replace too, but a partition is never resized or
// moved in place: the replacement could only be made once the old partition is gone, so unless the
// state already says deletePartition the diff refuses, before anything is switched off. Priority,
// discard, persistence and deletePartition update in place, as does a swap that is off or a
// partition with no swap signature.
func (SwapPartition) Diff(_ context.Context, req infer.DiffRequest[SwapPartitionArgs, SwapPartitionState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	wanted := swapPartDeclared(a)
	var replaces []string
	if old.Name != a.Name {
		replaces = append(replaces, "name")
	}
	if old.Disk != "" && old.Disk != a.Disk {
		replaces = append(replaces, "disk")
	}
	if int64(old.SizeBytes) != int64(a.SizeGib)<<30 {
		replaces = append(replaces, "sizeGib")
	}
	if a.Start != nil && old.StartSector != *a.Start {
		replaces = append(replaces, "start")
	}
	if moved := moves(replaces); len(moved) > 0 && !old.DeletePartition {
		return p.DiffResponse{}, fmt.Errorf("%s (%s) would have to be replaced to change %s, and SwapPartition never resizes or moves a partition in place. "+
			"Keep the old values, or deploy deletePartition: true first so a replacement may remove the old partition (the swap on it is switched off and its contents lost)",
			old.Name, old.Device, strings.Join(moved, ", "))
	}
	var unitWanted *string
	if wanted.Persistent {
		u := swappart.Unit(a.Name, a.Priority, a.Discard)
		unitWanted = &u
	}
	priorityNow := old.Priority
	if old.ActivePriority != nil {
		priorityNow = *old.ActivePriority
	}
	changed := !old.Active || priorityNow != a.Priority || old.Signature != "swap" ||
		deref(old.Discard) != deref(a.Discard) || (old.Discard == nil) != (a.Discard == nil) ||
		old.Persistent != wanted.Persistent || old.DeletePartition != wanted.DeletePartition ||
		!sameString(old.UnitFile, unitWanted)
	return diffOf(changed, true, replaces...), nil
}

// moves is the replaced properties that change the partition itself rather than which one it is.
func moves(replaces []string) []string {
	var out []string
	for _, r := range replaces {
		if r != "name" {
			out = append(out, r)
		}
	}
	return out
}

// deleteSwapPartCommand wipes and removes partition num, under the disk's lock and only after
// checking, inside that lock, that the entry at num still has the recorded GPT id and name: the
// read the resource acted on was taken before the lock, and the table can change in between.
func deleteSwapPartCommand(disk string, num int, node, name, partUUID string) string {
	d, n, nd := shellQuote(disk), strconv.Itoa(num), shellQuote(node)
	inner := `g=$(sfdisk --part-uuid ` + d + ` ` + n + ` 2>/dev/null | tr a-z A-Z); l=$(sfdisk --part-label ` + d + ` ` + n + ` 2>/dev/null); ` +
		`if [ "$g" != ` + shellQuote(strings.ToUpper(partUUID)) + ` ] || [ "$l" != ` + shellQuote(name) + ` ]; then ` +
		`echo "partition `+n+` of "` + d + `" is now $l ($g), not the recorded one; it is left alone" >&2; exit 1; fi; ` +
		`wipefs -q -a ` + nd + ` && sgdisk -d ` + n + ` ` + d + ` >/dev/null`
	return "flock " + d + " sh -c " + shellQuote(inner) + " && { partx -d --nr " + n + " " + d + " 2>/dev/null || true; }"
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Delete turns the swap off (if the memory to take its pages back is there) and removes the unit.
// The partition stays, with its data, unless deletePartition is set, and then only the partition
// with the GPT id this resource recorded is removed.
func (SwapPartition) Delete(ctx context.Context, req infer.DeleteRequest[SwapPartitionState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	s := req.State
	name := s.Name
	if name == "" {
		name = req.ID
	}
	unitPath, unit := shellQuote(swapPartUnitPath(name)), shellQuote(swappart.UnitName(swappart.What(name)))
	obs, _, ok, err := locateSwapPart(ctx, m, s.Disk, name)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if !ok {
		_, err := m.must(ctx, m.escalate("if [ -e "+unitPath+" ]; then systemctl disable "+unit+" 2>&1; rm -f "+unitPath+" && systemctl daemon-reload; fi"))
		return infer.DeleteResponse{}, err
	}
	node := obs.node()
	if a := obs.detail.active; a != nil {
		if err := swappart.SafeToSwapoff(node, a.Used, obs.detail.memAvailable); err != nil {
			return infer.DeleteResponse{}, err
		}
	}
	n := shellQuote(node)
	steps := []string{}
	if obs.detail.unitFile != nil {
		steps = append(steps, "systemctl disable --now "+unit+" 2>&1; rm -f "+unitPath+" && systemctl daemon-reload")
	}
	steps = append(steps, "if swapon --show=NAME --noheadings --raw | grep -qxF "+n+"; then swapoff "+n+"; fi")
	if s.DeletePartition {
		part, table := obs.found.Partition, obs.found.Table
		if s.PartUuid == "" || !strings.EqualFold(part.UUID, s.PartUuid) {
			return infer.DeleteResponse{}, fmt.Errorf("%s (%s) has GPT id %s, not the %s this resource recorded; the partition is left alone", node, name, part.UUID, s.PartUuid)
		}
		steps = append(steps, deleteSwapPartCommand(table.Path, part.Number, node, name, s.PartUuid))
	}
	for _, step := range steps {
		if _, err := m.must(ctx, m.escalate(step)); err != nil {
			return infer.DeleteResponse{}, err
		}
	}
	return infer.DeleteResponse{}, nil
}
