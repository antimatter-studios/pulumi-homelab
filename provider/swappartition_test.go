package homelab

import (
	"os"
	"strconv"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
	"github.com/antimatter-studios/pulumi-homelab/provider/swap"
	"github.com/antimatter-studios/pulumi-homelab/provider/swappart"
)

// swapMachine is the Ryzen as a scripted fake: its real partition tables (testdata, read on
// 2026-11-28), and for swap-nvme0 a signature, swap areas, a unit file and free memory that the
// commands the provider sends change as they would on the machine.
type swapMachine struct {
	scan      string
	blkid     string
	blkidCode int
	swaps     string
	unit      *string
	memKiB    int
}

func ryzenMachine(t *testing.T) *swapMachine {
	t.Helper()
	scan, err := os.ReadFile("swappart/testdata/ryzen-scan.txt")
	if err != nil {
		t.Fatal(err)
	}
	detail, err := os.ReadFile("swappart/testdata/ryzen-detail-nvme0.txt")
	if err != nil {
		t.Fatal(err)
	}
	probe, _, _ := strings.Cut(string(detail), "#pulumi-homelab#blkid")
	return &swapMachine{scan: string(scan), blkid: probe, swaps: "/dev/zram0 partition 8589930496 2886524928 100\n/dev/nvme0n1p3 partition 34359734272 0 10\n/dev/nvme1n1p3 partition 34359734272 0 10\n", memKiB: 20676044}
}

func (s *swapMachine) detail() string {
	out := s.blkid + swapPartBlkid + itoa(s.blkidCode) + "\n" + swapPartSwaps + "\n" + s.swaps + swapPartUnit + "\n"
	if s.unit != nil {
		out += *s.unit + swapPartFound + "\n"
	}
	return out + swapPartMem + "\n" + itoa(s.memKiB) + "\n"
}

func itoa(n int) string { return strconv.Itoa(n) }

const nvme0Swap = "/dev/nvme0n1p3 partition 34359734272 0 10\n"

func (s *swapMachine) runner() *remotetest.Runner {
	fake := remotetest.New()
	fake.When(func(c string) bool { return true }, func(c string) remotetest.Reply {
		switch {
		case strings.Contains(c, "sgdisk -n"), strings.Contains(c, "sgdisk -d"), strings.Contains(c, "wipefs"), strings.Contains(c, "partx -d"):
			return remotetest.Reply{}
		case strings.Contains(c, "mkswap -L"):
			s.blkid, s.blkidCode = "TYPE=swap\nLABEL=swap-nvme0\n", 0
			return remotetest.Reply{}
		case strings.Contains(c, "sfdisk --json"):
			return remotetest.Reply{Stdout: s.scan}
		case strings.Contains(c, "blkid -p -o export"):
			return remotetest.Reply{Stdout: s.detail()}
		case strings.Contains(c, "cat > "):
			body := c[strings.Index(c, "PULUMI_EOF")+len("PULUMI_EOF"):]
			body = body[strings.Index(body, "\n")+1 : strings.LastIndex(body, "PULUMI_EOF")]
			s.unit = &body
			return remotetest.Reply{}
		case strings.Contains(c, "systemctl disable --now"):
			s.unit, s.swaps = nil, strings.Replace(s.swaps, nvme0Swap, "", 1)
			return remotetest.Reply{}
		case strings.Contains(c, "systemctl disable"):
			s.unit = nil
			return remotetest.Reply{}
		case strings.Contains(c, "systemctl start"), strings.Contains(c, "swapon -p"):
			s.swaps += nvme0Swap
			return remotetest.Reply{}
		case strings.Contains(c, "systemctl restart"):
			s.swaps = strings.Replace(s.swaps, nvme0Swap, "/dev/nvme0n1p3 partition 34359734272 0 7\n", 1)
			return remotetest.Reply{}
		case strings.Contains(c, "swapoff"), strings.Contains(c, "systemctl is-enabled"), strings.Contains(c, "if [ -e"):
			s.swaps = strings.Replace(s.swaps, nvme0Swap, "", 1)
			return remotetest.Reply{}
		}
		return remotetest.Reply{Code: 99, Stderr: "unscripted: " + c}
	}, 0)
	return fake
}

func sent(fake *remotetest.Runner, part string) bool {
	for _, c := range fake.Commands() {
		if strings.Contains(c, part) {
			return true
		}
	}
	return false
}

// The program homelab-server declares for each drive, to adopt the hand-made partitions while the
// old script still enables them at boot.
func adoption(extra ...any) property.Map {
	return sm(append([]any{"disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10, "persistent", false}, extra...)...)
}

func TestSwapPartitionImportOfTheRyzenShowsNoChange(t *testing.T) {
	machine := ryzenMachine(t)
	fake := machine.runner()
	s := configured(t, fake)
	urn := urnOf("SwapPartition", "imported")
	read, err := s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn})
	if err != nil || read.ID != "swap-nvme0" {
		t.Fatalf("%+v %v", read, err)
	}
	in := read.Inputs
	if in.Get("disk").AsString() != "/dev/nvme0n1" || in.Get("sizeGib").AsNumber() != 32 || in.Get("priority").AsNumber() != 10 ||
		in.Get("persistent").AsBool() || in.Get("discard").IsString() {
		t.Fatalf("imported inputs %v", in)
	}
	st := read.Properties
	if st.Get("device").AsString() != "/dev/nvme0n1p3" || st.Get("signature").AsString() != "swap" || !st.Get("active").AsBool() ||
		st.Get("partUuid").AsString() != "28DE59C8-13C6-4D21-A2B8-67DEA142ED23" || st.Get("unit").AsString() != `dev-disk-by\x2dpartlabel-swap\x2dnvme0.swap` {
		t.Fatalf("state %v", st)
	}
	d, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: st, Inputs: adoption()})
	if err != nil || d.HasChanges {
		t.Fatalf("the adoption shows a change: %+v %v", d, err)
	}
	// only reads went to the machine
	for _, c := range fake.Commands() {
		if !strings.Contains(c, "sfdisk --json") && !strings.Contains(c, "blkid -p -o export") {
			t.Errorf("an import ran %q", c)
		}
	}
	// <disk>:<name> names the disk as the program does, and the id is still the name
	byID, err := s.Read(p.ReadRequest{ID: "/dev/disk/by-id/nvme-x:swap-nvme0", Urn: urn})
	if err != nil || byID.ID != "swap-nvme0" || byID.Inputs.Get("disk").AsString() != "/dev/disk/by-id/nvme-x" || byID.Properties.Get("diskDevice").AsString() != "/dev/nvme0n1" {
		t.Fatalf("%+v %v", byID, err)
	}
	// the next step, persistence on, is an in-place update
	d, err = s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: st, Inputs: sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10, "discard", "once")})
	if err != nil || !d.HasChanges || len(swapReplaces(d)) > 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

// Taking over boot from the old script: the unit is written and enabled, the swap (already on at
// the declared priority) is not touched, and a refresh after shows no change.
func TestSwapPartitionPersistenceOnLeavesTheSwapAlone(t *testing.T) {
	machine := ryzenMachine(t)
	fake := machine.runner()
	s := configured(t, fake)
	urn := urnOf("SwapPartition", "it")
	read, _ := s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn})
	inputs := sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10, "discard", "once")
	before := len(fake.Commands())
	updated, err := s.Update(p.UpdateRequest{ID: "swap-nvme0", Urn: urn, State: read.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	writes := strings.Join(fake.Commands()[before:], "\n")
	if !strings.Contains(writes, "systemctl enable") || strings.Contains(writes, "swapoff") || strings.Contains(writes, "restart") ||
		strings.Contains(writes, "systemctl start") || strings.Contains(writes, "mkswap") || strings.Contains(writes, "sgdisk") {
		t.Fatalf("%s", writes)
	}
	if machine.unit == nil || *machine.unit != swappart.Unit("swap-nvme0", 10, sp("once")) {
		t.Fatalf("unit %v", machine.unit)
	}
	refreshed, err := s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn, Properties: updated.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "swap-nvme0", Urn: urn, State: refreshed.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("%+v %v", d, err)
	}
	// a priority change restarts the unit, once the pages are known to fit in memory
	machine.swaps = strings.Replace(machine.swaps, nvme0Swap, "/dev/nvme0n1p3 partition 34359734272 1073741824 10\n", 1)
	refreshed, _ = s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn, Properties: refreshed.Properties, Inputs: inputs})
	machine.memKiB = 1 << 20 // 1 GiB available, 1 GiB in swap
	seven := sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 7, "discard", "once")
	if _, err := s.Update(p.UpdateRequest{ID: "swap-nvme0", Urn: urn, State: refreshed.Properties, Inputs: seven}); err == nil || !strings.Contains(err.Error(), "could run the machine out of memory") {
		t.Fatalf("%v", err)
	}
	if sent(fake, "systemctl restart") {
		t.Fatal("restarted with too little memory")
	}
	machine.memKiB = 20676044
	if _, err := s.Update(p.UpdateRequest{ID: "swap-nvme0", Urn: urn, State: refreshed.Properties, Inputs: seven}); err != nil {
		t.Fatal(err)
	}
	if !sent(fake, "systemctl restart") || !strings.Contains(*machine.unit, "Priority=7") {
		t.Fatalf("%v", fake.Commands())
	}
}

func TestSwapPartitionDiff(t *testing.T) {
	machine := ryzenMachine(t)
	s := configured(t, machine.runner())
	urn := urnOf("SwapPartition", "it")
	read, _ := s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn})
	st := read.Properties
	diff := func(inputs property.Map) (p.DiffResponse, error) {
		return s.Diff(p.DiffRequest{ID: "swap-nvme0", Urn: urn, State: st, Inputs: inputs})
	}
	for name, inputs := range map[string]property.Map{
		"priority": adoption("priority", 20),
		"discard":  adoption("discard", "pages"),
		"persist":  adoption("persistent", true),
		"delete":   adoption("deletePartition", true),
	} {
		if d, err := diff(inputs); err != nil || !d.HasChanges || len(swapReplaces(d)) > 0 {
			t.Errorf("%s: %+v %v", name, d, err)
		}
	}
	// another name is another partition: replaced, the old one kept
	if d, err := diff(sm("disk", "/dev/nvme0n1", "name", "swap-x", "sizeGib", 32, "priority", 10, "persistent", false)); err != nil || !d.DeleteBeforeReplace || len(swapReplaces(d)) != 1 {
		t.Errorf("name: %+v %v", d, err)
	}
	// size, disk and start would need the partition resized or moved: refused at preview
	for name, inputs := range map[string]property.Map{
		"shrink": adoption("sizeGib", 16),
		"grow":   adoption("sizeGib", 64),
		"disk":   sm("disk", "/dev/nvme1n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10, "persistent", false),
		"start":  adoption("start", 2048),
	} {
		if _, err := diff(inputs); err == nil || !strings.Contains(err.Error(), "never resizes or moves a partition in place") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// declaring the start it already has is no change
	if d, err := diff(adoption("start", 3124793344)); err != nil || d.HasChanges {
		t.Errorf("same start: %+v %v", d, err)
	}
	// once deletePartition is in the state, the replace goes ahead, delete first
	st = st.Set("deletePartition", property.New(true))
	if d, err := diff(adoption("sizeGib", 16, "deletePartition", true)); err != nil || !d.DeleteBeforeReplace || len(swapReplaces(d)) != 1 {
		t.Errorf("allowed shrink: %+v %v", d, err)
	}
}

func TestSwapPartitionCreateRefuses(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*swapMachine)
		inputs property.Map
		want   string
	}{
		{"no free space after the last partition", nil, sm("disk", "/dev/nvme0n1", "name", "swap-new", "sizeGib", 8, "priority", 10), "does not fit on /dev/nvme0n1"},
		{"a start inside the btrfs root", nil, sm("disk", "/dev/nvme0n1", "name", "swap-new", "sizeGib", 1, "priority", 10, "start", 2099200+4096), `would overlap partition 2 (/dev/nvme0n1p2, "homelab-nvme"`},
		{"a start inside the EFI partition", nil, sm("disk", "/dev/nvme0n1", "name", "swap-new", "sizeGib", 1, "priority", 10, "start", 4096), `would overlap partition 1 (/dev/nvme0n1p1, "EFI"`},
		{"the name on the other drive", nil, sm("disk", "/dev/nvme0n1", "name", "swap-nvme1", "sizeGib", 32, "priority", 10), "already exists on /dev/nvme1n1"},
		{"the name at another size", nil, sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 16, "priority", 10), "never resizes a partition"},
		{"a partition of another type", nil, sm("disk", "/dev/nvme0n1", "name", "EFI", "sizeGib", 1, "priority", 10), "is left alone"},
		{"a filesystem on it", func(s *swapMachine) { s.blkid = "TYPE=ext4\nUUID=abc\n" }, sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10), "holds a ext4 signature"},
		{"a probe that fails", func(s *swapMachine) { s.blkid, s.blkidCode = "", 8 }, sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10), "could not probe"},
		{"a bad name", nil, sm("disk", "/dev/nvme0n1", "name", "much-too-long-for-a-label", "sizeGib", 1, "priority", 10), "must be 1-16 characters"},
		{"a bad discard", nil, sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10, "discard", "always"), "must be once, pages or both"},
	}
	for _, c := range cases {
		machine := ryzenMachine(t)
		machine.swaps = strings.Replace(machine.swaps, nvme0Swap, "", 1)
		if c.setup != nil {
			c.setup(machine)
		}
		fake := machine.runner()
		s := configured(t, fake)
		_, err := s.Create(p.CreateRequest{Urn: urnOf("SwapPartition", "it"), Properties: c.inputs})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
		for _, write := range []string{"sgdisk", "mkswap", "wipefs", "cat > ", "systemctl", "swapon -p", "swapoff"} {
			if sent(fake, write) {
				t.Errorf("%s: ran %s", c.name, write)
			}
		}
	}
}

// A blank partition already named and sized as declared is formatted and switched on, without
// partitioning anything.
func TestSwapPartitionCreateFormatsABlankPartition(t *testing.T) {
	machine := ryzenMachine(t)
	machine.blkid, machine.blkidCode = "", 2
	machine.swaps = strings.Replace(machine.swaps, nvme0Swap, "", 1)
	fake := machine.runner()
	s := configured(t, fake)
	created, err := s.Create(p.CreateRequest{Urn: urnOf("SwapPartition", "it"), Properties: sm("disk", "/dev/nvme0n1", "name", "swap-nvme0", "sizeGib", 32, "priority", 10)})
	if err != nil {
		t.Fatal(err)
	}
	if !sent(fake, "mkswap -L") || sent(fake, "sgdisk") || !sent(fake, "systemctl start") || sent(fake, "swapon -p") {
		t.Fatalf("%v", fake.Commands())
	}
	if !created.Properties.Get("active").AsBool() || created.Properties.Get("signature").AsString() != "swap" {
		t.Fatalf("%v", created.Properties)
	}
	// persistent off: swapon directly, no unit
	machine = ryzenMachine(t)
	machine.swaps = strings.Replace(machine.swaps, nvme0Swap, "", 1)
	fake = machine.runner()
	s = configured(t, fake)
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("SwapPartition", "it"), Properties: adoption("discard", "once")}); err != nil {
		t.Fatal(err)
	}
	if !sent(fake, "swapon -p 10 --discard=once") || sent(fake, "cat > ") || machine.unit != nil {
		t.Fatalf("%v", fake.Commands())
	}
}

func TestSwapPartitionCreateCommandHoldsTheLockAndChecksTheTable(t *testing.T) {
	table := swappart.Table{Path: "/dev/sdx", Hash: "abc", Label: "gpt"}
	c := createSwapPartCommand(table, swappart.Placement{Number: 3, Start: 2048, End: 4095}, "swap-x")
	for _, want := range []string{"flock '/dev/sdx' sh -c ", "sha256sum", "abc", "sgdisk -n 3:2048:4095 -t 3:8200 -c 3:", "partx -a --nr 3 '/dev/sdx'", "[ -e '/dev/sdx3' ]"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in %s", want, c)
		}
	}
	if strings.Contains(c, "sgdisk -d") || strings.Contains(c, "sgdisk -o") || strings.Contains(c, "--zap") {
		t.Errorf("%s", c)
	}
}

// The partition is checked again inside the lock it is deleted under, before anything is wiped.
func TestSwapPartitionDeleteCommandChecksInsideTheLock(t *testing.T) {
	c := deleteSwapPartCommand("/dev/sdx", 3, "/dev/sdx3", "swap-x", "28de59c8-13c6-4d21-a2b8-67dea142ed23")
	inside, after, ok := strings.Cut(strings.TrimPrefix(c, "flock '/dev/sdx' sh -c "), " && { partx -d")
	if !ok || !strings.HasPrefix(c, "flock '/dev/sdx' sh -c ") {
		t.Fatalf("%s", c)
	}
	check := strings.Index(inside, "sfdisk --part-uuid")
	label := strings.Index(inside, "sfdisk --part-label")
	wipe := strings.Index(inside, "wipefs -q -a")
	del := strings.Index(inside, "sgdisk -d 3")
	if check < 0 || label < 0 || wipe < check || wipe < label || del < wipe || !strings.Contains(inside, "28DE59C8-13C6-4D21-A2B8-67DEA142ED23") || !strings.Contains(after, "--nr 3") {
		t.Fatalf("%s", c)
	}
}

func TestSwapPartitionDelete(t *testing.T) {
	machine := ryzenMachine(t)
	unit := swappart.Unit("swap-nvme0", 10, nil)
	machine.unit = &unit
	fake := machine.runner()
	s := configured(t, fake)
	urn := urnOf("SwapPartition", "it")
	read, _ := s.Read(p.ReadRequest{ID: "swap-nvme0", Urn: urn})
	st := read.Properties

	// too little memory for the pages in it: nothing is done
	machine.swaps = strings.Replace(machine.swaps, nvme0Swap, "/dev/nvme0n1p3 partition 34359734272 4294967296 10\n", 1)
	machine.memKiB = 2 << 20
	if err := s.Delete(p.DeleteRequest{ID: "swap-nvme0", Urn: urn, Properties: st}); err == nil || !strings.Contains(err.Error(), "left on") {
		t.Fatalf("%v", err)
	}
	if sent(fake, "systemctl disable") || sent(fake, "swapoff") {
		t.Fatal("swapped off with too little memory")
	}

	// by default: swap off, unit gone, the partition kept
	machine.swaps, machine.memKiB = ryzenMachine(t).swaps, 20676044
	if err := s.Delete(p.DeleteRequest{ID: "swap-nvme0", Urn: urn, Properties: st}); err != nil {
		t.Fatal(err)
	}
	if !sent(fake, "systemctl disable --now") || sent(fake, "sgdisk") || sent(fake, "wipefs") || machine.unit != nil || strings.Contains(machine.swaps, "nvme0n1p3") {
		t.Fatalf("%v", fake.Commands())
	}

	// a unit at that path that this provider did not write is left, with the swap switched off
	foreign := "[Swap]\nWhat=/dev/nvme0n1p3\n"
	machine.unit = &foreign
	machine.swaps = ryzenMachine(t).swaps
	before := len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: "swap-nvme0", Urn: urn, Properties: st}); err != nil {
		t.Fatal(err)
	}
	later := strings.Join(fake.Commands()[before:], "\n")
	if strings.Contains(later, "systemctl disable") || strings.Contains(later, "rm -f") || machine.unit == nil || !strings.Contains(later, "swapoff") {
		t.Fatalf("%s", later)
	}
	machine.unit = nil

	// deletePartition, but the partition by that name is not the one recorded: left alone
	other := st.Set("deletePartition", property.New(true)).Set("partUuid", property.New("00000000-0000-0000-0000-000000000000"))
	if err := s.Delete(p.DeleteRequest{ID: "swap-nvme0", Urn: urn, Properties: other}); err == nil || !strings.Contains(err.Error(), "left alone") {
		t.Fatalf("%v", err)
	}
	if sent(fake, "sgdisk") {
		t.Fatal("deleted a partition it did not record")
	}
	// the recorded one: wiped and removed, by number, and nothing else
	if err := s.Delete(p.DeleteRequest{ID: "swap-nvme0", Urn: urn, Properties: st.Set("deletePartition", property.New(true))}); err != nil {
		t.Fatal(err)
	}
	if !sent(fake, "sfdisk --part-uuid") || !sent(fake, "wipefs -q -a") || !sent(fake, "sgdisk -d 3 ") || !sent(fake, "28DE59C8-13C6-4D21-A2B8-67DEA142ED23") {
		t.Fatalf("%v", fake.Commands())
	}
}

// The Swap resource homelab-server declares (enabled false, otherSwap allow) does nothing about
// swap partitions: they are not its path and they are not in fstab.
func TestSwapLeavesSwapPartitionsAlone(t *testing.T) {
	before := swapBefore{
		active: []swap.Active{{Path: "/dev/zram0", Kind: "partition", SizeMB: 8192}, {Path: "/dev/nvme0n1p3", Kind: "partition", SizeMB: 32768}, {Path: "/dev/nvme1n1p3", Kind: "partition", SizeMB: 32768}},
		dphys:  sp("masked"),
	}
	if got := swapAction(before, swapWanted{path: "/var/swap", otherSwap: "allow"}); got != "nothing" {
		t.Fatalf("got %s", got)
	}
}

// sm is a property map from key/value pairs: strings, ints and bools.
func sm(kv ...any) property.Map {
	out := map[string]property.Value{}
	for i := 0; i < len(kv); i += 2 {
		switch v := kv[i+1].(type) {
		case int:
			out[kv[i].(string)] = property.New(float64(v))
		case string:
			out[kv[i].(string)] = property.New(v)
		case bool:
			out[kv[i].(string)] = property.New(v)
		}
	}
	return property.NewMap(out)
}

func swapReplaces(d p.DiffResponse) []string {
	var out []string
	for k, v := range d.DetailedDiff {
		if v.Kind == p.UpdateReplace {
			out = append(out, k)
		}
	}
	return out
}

// A state with no partition seen yet (a preview's) is no change for the same declaration, and a
// plain replace, never a refusal, for another size: there is no partition to protect.
func TestSwapPartitionDiffBeforeAnythingWasSeen(t *testing.T) {
	s := configured(t, remotetest.New())
	urn := urnOf("SwapPartition", "it")
	inputs := sm("disk", "/dev/sdx", "name", "swap-x", "sizeGib", 1, "priority", 10)
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "swap-x", Urn: urn, State: created.Properties, Inputs: inputs}); err != nil || len(swapReplaces(d)) > 0 {
		t.Fatalf("%+v %v", d, err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "swap-x", Urn: urn, State: created.Properties, Inputs: sm("disk", "/dev/sdx", "name", "swap-x", "sizeGib", 2, "priority", 10)}); err != nil || len(swapReplaces(d)) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
}
