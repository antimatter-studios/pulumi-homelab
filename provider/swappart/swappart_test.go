package swappart

import (
	"os"
	"strings"
	"testing"
)

func ryzen(t *testing.T) []Table {
	t.Helper()
	out, err := os.ReadFile("testdata/ryzen-scan.txt")
	if err != nil {
		t.Fatal(err)
	}
	tables, err := ParseScan(string(out))
	if err != nil {
		t.Fatal(err)
	}
	return tables
}

func i64(v int64) *int64   { return &v }
func str(v string) *string { return &v }

// The scan the Ryzen gave on 2026-11-28: the declared disk first, then lsblk's disks, of which
// nvme0n1 comes again (kept once) and zram0 has no partition table.
func TestParseScanOfTheRyzen(t *testing.T) {
	tables := ryzen(t)
	if len(tables) != 3 || tables[0].Path != "/dev/nvme0n1" || tables[1].Path != "/dev/zram0" || tables[2].Path != "/dev/nvme1n1" {
		t.Fatalf("%+v", tables)
	}
	if tables[1].Label != "" || len(tables[1].Partitions) != 0 {
		t.Errorf("zram0: %+v", tables[1])
	}
	n0 := tables[0]
	if n0.Label != "gpt" || n0.SectorSize != 512 || n0.FirstLBA != 34 || n0.LastLBA != 3907029134 || len(n0.Partitions) != 4 ||
		n0.Hash != "3642b491bab6cd728e7046265197a85dfac31597cd6c12324593a30727c1ffe6" {
		t.Fatalf("%+v", n0)
	}
	p3 := n0.Partitions[2]
	if p3.Number != 3 || p3.Node != "/dev/nvme0n1p3" || p3.Name != "swap-nvme0" || p3.Type != SwapType || p3.Size*512 != 32<<30 {
		t.Errorf("%+v", p3)
	}
}

func TestFind(t *testing.T) {
	tables := ryzen(t)
	f, ok, err := Find(tables, "swap-nvme1", "")
	if err != nil || !ok || f.Table.Path != "/dev/nvme1n1" || f.Partition.Node != "/dev/nvme1n1p3" {
		t.Fatalf("%+v %v %v", f, ok, err)
	}
	// found on another disk than the declared one: still found, the name is the identity
	if f, ok, err = Find(tables, "swap-nvme1", "/dev/nvme0n1"); err != nil || !ok || f.Table.Path != "/dev/nvme1n1" {
		t.Fatalf("%+v %v %v", f, ok, err)
	}
	if _, ok, err := Find(tables, "nothing", ""); ok || err != nil {
		t.Fatalf("%v %v", ok, err)
	}
	// both drives have a partition named cache: ambiguous, unless the declared disk settles it
	if _, _, err := Find(tables, "cache", ""); err == nil || !strings.Contains(err.Error(), "/dev/nvme0n1p4, /dev/nvme1n1p4") {
		t.Fatalf("%v", err)
	}
	if f, ok, err := Find(tables, "cache", "/dev/nvme1n1"); err != nil || !ok || f.Partition.Node != "/dev/nvme1n1p4" {
		t.Fatalf("%+v %v %v", f, ok, err)
	}
}

func TestNumberAndNodeNames(t *testing.T) {
	cases := []struct {
		disk, node string
		n          int
	}{
		{"/dev/nvme0n1", "/dev/nvme0n1p3", 3},
		{"/dev/sda", "/dev/sda12", 12},
		{"/dev/loop0", "/dev/loop0p1", 1},
		{"/disk.img", "/disk.img4", 4},
		{"/dev/disk/by-id/x", "/dev/disk/by-id/x-part2", 2},
	}
	for _, c := range cases {
		if got := NumberOf(c.disk, c.node); got != c.n {
			t.Errorf("NumberOf(%s, %s) = %d", c.disk, c.node, got)
		}
	}
	if NodeOf("/dev/nvme0n1", 5) != "/dev/nvme0n1p5" || NodeOf("/dev/sda", 5) != "/dev/sda5" || NodeOf("/disk.img", 3) != "/disk.img3" {
		t.Error("NodeOf")
	}
}

func disk() Table {
	// 4 GiB of 512-byte sectors: an EFI partition and a root partition, then free space
	return Table{Path: "/dev/sdx", Label: "gpt", FirstLBA: 34, LastLBA: 8388574, SectorSize: 512, Partitions: []Partition{
		{Node: "/dev/sdx1", Number: 1, Start: 2048, Size: 1048576, Name: "EFI"},
		{Node: "/dev/sdx2", Number: 2, Start: 1050624, Size: 2097152, Name: "root"},
	}}
}

func TestPlanAfterTheLastPartition(t *testing.T) {
	got, err := Plan(disk(), 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// root ends at 3147775; the next 1 MiB boundary is 3147776
	if got != (Placement{Number: 3, Start: 3147776, End: 3147776 + 2097152 - 1}) {
		t.Errorf("%+v", got)
	}
	// a hole in the numbers is filled first
	d := disk()
	d.Partitions[0].Number = 5
	if got, _ := Plan(d, 1, nil); got.Number != 1 {
		t.Errorf("%+v", got)
	}
	// 4096-byte sectors: sizes and alignment in those
	d = Table{Path: "/dev/sdy", Label: "gpt", FirstLBA: 6, LastLBA: 1 << 20, SectorSize: 4096}
	if got, err := Plan(d, 1, nil); err != nil || got.Start != 256 || got.End != 256+262144-1 {
		t.Errorf("%+v %v", got, err)
	}
}

func TestPlanAtAStart(t *testing.T) {
	// the gap before the EFI partition is free space too, when asked for by sector
	d := disk()
	d.Partitions[0].Start = 2048 + 2097152
	d.Partitions[0].Size = 1
	d.Partitions[1].Start = 2048 + 2097152 + 2048
	if got, err := Plan(d, 1, i64(2048)); err != nil || got.Start != 2048 || got.End != 2048+2097152-1 {
		t.Fatalf("%+v %v", got, err)
	}
}

// Every refusal: nothing is planned, so nothing is written.
func TestPlanRefuses(t *testing.T) {
	noTable := disk()
	noTable.Label = ""
	dos := disk()
	dos.Label = "dos"
	odd := disk()
	odd.SectorSize = 4096 * 3
	cases := []struct {
		name  string
		table Table
		size  int
		start *int64
		want  string
	}{
		{"a disk with no partition table", noTable, 1, nil, "has no partition table"},
		{"an MBR disk", dos, 1, nil, "has a dos partition table"},
		{"overlap with the root partition", disk(), 1, i64(1050624 + 100), `would overlap partition 2 (/dev/sdx2, "root"`},
		{"overlap reaching into the EFI partition", disk(), 1, i64(40), `would overlap partition 1`},
		{"no room after the last partition", disk(), 3, nil, "does not fit on /dev/sdx"},
		{"past the end of the disk", disk(), 1, i64(8388000), "does not fit"},
		{"before the first usable sector", disk(), 1, i64(2), "before /dev/sdx's first usable sector"},
		{"a size that is not whole sectors", odd, 1, nil, "not a whole number"},
		{"no size", disk(), 0, nil, "at least 1"},
	}
	for _, c := range cases {
		if _, err := Plan(c.table, c.size, c.start); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// the Ryzen's drives are full: p4 runs to the last sector
	if _, err := Plan(ryzen(t)[0], 32, nil); err == nil || !strings.Contains(err.Error(), "does not fit on /dev/nvme0n1") {
		t.Errorf("ryzen: %v", err)
	}
}

func TestParseBlkid(t *testing.T) {
	detail, _ := os.ReadFile("testdata/ryzen-detail-nvme0.txt")
	probe, _, _ := strings.Cut(string(detail), "#pulumi-homelab#blkid")
	s, err := ParseBlkid(probe, 0)
	if err != nil || s.Kind != "swap" || s.Label != "swap-nvme0" || s.UUID != "2b6ec10d-21d8-40b5-93d4-464443935aee" {
		t.Fatalf("%+v %v", s, err)
	}
	// the partition-entry values blkid also prints are not a signature
	if s, err := ParseBlkid("DEVNAME=/dev/nvme0n1p4\nPART_ENTRY_SCHEME=gpt\nPART_ENTRY_NAME=cache\n", 0); err != nil || s.Kind != "" {
		t.Errorf("%+v %v", s, err)
	}
	if s, err := ParseBlkid("", 2); err != nil || s.Kind != "" {
		t.Errorf("%+v %v", s, err)
	}
	if s, _ := ParseBlkid("TYPE=ext4\nUUID=x\n", 0); s.Kind != "ext4" {
		t.Errorf("%+v", s)
	}
	if s, _ := ParseBlkid("PTTYPE=dos\n", 0); s.Kind != "partition table dos" {
		t.Errorf("%+v", s)
	}
	// ambiguous probes (exit 8) and errors are never "blank"
	if _, err := ParseBlkid("", 8); err == nil {
		t.Error("exit 8 read as blank")
	}
}

func TestParseSwaps(t *testing.T) {
	swaps := ParseSwaps("/dev/zram0 partition 8589930496 2886524928 100\n/dev/nvme0n1p3 partition 34359734272 4096 10\n/var/my\\x20swap file 1024 0 -2\n")
	if len(swaps) != 3 || swaps[1] != (Active{Path: "/dev/nvme0n1p3", Priority: 10, Size: 34359734272, Used: 4096}) || swaps[2].Path != "/var/my swap" || swaps[2].Priority != -2 {
		t.Fatalf("%+v", swaps)
	}
	if a, ok := LookupActive(swaps, "/dev/nvme0n1p3"); !ok || a.Priority != 10 {
		t.Error(a, ok)
	}
	if _, ok := LookupActive(swaps, "/dev/nvme1n1p3"); ok {
		t.Error("found a swap that is not on")
	}
}

func TestValidation(t *testing.T) {
	for _, ok := range []string{"swap-nvme0", "a", "swap_1.x", "0123456789abcdef"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", ".x", "0123456789abcdefg", "has space", "a/b", "é"} {
		if ValidName(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if ValidDiscard(str("once")) != nil || ValidDiscard(nil) != nil || ValidDiscard(str("yes")) == nil {
		t.Error("discard")
	}
	if ValidPriority(0) != nil || ValidPriority(32767) != nil || ValidPriority(-1) == nil || ValidPriority(32768) == nil {
		t.Error("priority")
	}
}

func TestUnitName(t *testing.T) {
	// what systemd-escape -p --suffix=swap prints, and what systemd lists on the Ryzen
	cases := map[string]string{
		"/dev/disk/by-partlabel/swap-nvme0": `dev-disk-by\x2dpartlabel-swap\x2dnvme0.swap`,
		"/dev/nvme0n1p3":                    "dev-nvme0n1p3.swap",
		"//dev//x/":                         "dev-x.swap",
		"/.hidden/a b":                      `\x2ehidden-a\x20b.swap`,
		"/":                                 "-.swap",
	}
	for path, want := range cases {
		if got := UnitName(path); got != want {
			t.Errorf("UnitName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestUnitAndItsSettings(t *testing.T) {
	u := Unit("swap-nvme0", 10, str("once"))
	want := UnitMarker + "\n[Unit]\nDescription=Swap on the GPT partition swap-nvme0\n\n[Swap]\nWhat=/dev/disk/by-partlabel/swap-nvme0\nPriority=10\nOptions=discard=once\n\n[Install]\nWantedBy=swap.target\n"
	if u != want {
		t.Fatalf("%q", u)
	}
	if prio, d := UnitSettings(u); *prio != 10 || *d != "once" {
		t.Errorf("%v %v", prio, d)
	}
	if prio, d := UnitSettings(Unit("x", 0, nil)); *prio != 0 || d != nil {
		t.Errorf("%v %v", prio, d)
	}
	if _, d := UnitSettings(Unit("x", 1, str("both"))); *d != "both" {
		t.Errorf("%v", d)
	}
	if SwaponArgs(10, str("once")) != "-p 10 --discard=once" || SwaponArgs(5, nil) != "-p 5" || SwaponArgs(1, str("both")) != "-p 1 --discard" {
		t.Error("swapon args")
	}
}

func TestSafeToSwapoff(t *testing.T) {
	if err := SafeToSwapoff("/dev/x", 0, 1<<30); err != nil {
		t.Error(err)
	}
	if err := SafeToSwapoff("/dev/x", 2<<30, 3<<30); err != nil {
		t.Error(err)
	}
	if err := SafeToSwapoff("/dev/x", 3<<30, 3<<30); err == nil || !strings.Contains(err.Error(), "3072 MiB of swapped pages and only 3072 MiB") {
		t.Errorf("%v", err)
	}
}
