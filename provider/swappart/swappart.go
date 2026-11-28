// Package swappart holds the pure part of SwapPartition: reading sfdisk, blkid and swapon output,
// placing a new partition in free space (and refusing when there is none), and writing the systemd
// swap unit that brings the partition back at boot.
package swappart

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SwapType is the GPT partition type GUID for Linux swap (sgdisk's 8200).
const SwapType = "0657FD6D-A4AB-43C4-84E5-0933C84B4F4F"

// DiskMarker starts the section of a scan that belongs to one disk: the marker, a space, and the
// disk's resolved path.
const DiskMarker = "#pulumi-homelab#disk "

// HashMarker starts the line, inside a disk's section, that holds the sha256 of `sfdisk --dump`.
const HashMarker = "#pulumi-homelab#hash "

// Partition is one entry of a partition table, as sfdisk --json gives it, plus its number.
type Partition struct {
	Node   string `json:"node"`
	Start  int64  `json:"start"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Number int    `json:"-"`
}

// End is the partition's last sector.
func (p Partition) End() int64 { return p.Start + p.Size - 1 }

// Table is one disk's partition table, as sfdisk --json gives it. Path is the disk's resolved path
// and Hash the sha256 of its `sfdisk --dump`, both from the scan around the JSON. A disk with no
// partition table has an empty Label.
type Table struct {
	Path       string      `json:"-"`
	Hash       string      `json:"-"`
	Label      string      `json:"label"`
	Device     string      `json:"device"`
	FirstLBA   int64       `json:"firstlba"`
	LastLBA    int64       `json:"lastlba"`
	SectorSize int64       `json:"sectorsize"`
	Partitions []Partition `json:"partitions"`
}

// ParseSfdisk reads the output of `sfdisk --json <disk>`. Empty output (no partition table, or no
// such disk) is a Table with no label.
func ParseSfdisk(out string) (Table, error) {
	if strings.TrimSpace(out) == "" {
		return Table{}, nil
	}
	var doc struct {
		PartitionTable Table `json:"partitiontable"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return Table{}, fmt.Errorf("could not read sfdisk's output: %w", err)
	}
	t := doc.PartitionTable
	if t.SectorSize == 0 {
		t.SectorSize = 512
	}
	for i := range t.Partitions {
		t.Partitions[i].Number = NumberOf(t.Device, t.Partitions[i].Node)
		t.Partitions[i].Type = strings.ToUpper(t.Partitions[i].Type)
	}
	return t, nil
}

// NumberOf is the partition number in a node name: what follows the disk's name, and the `p` the
// kernel puts between them when the disk's name ends in a digit (nvme0n1p3, loop0p1, sda3), or
// udev's -part for a by-id path.
func NumberOf(disk, node string) int {
	rest := strings.TrimPrefix(node, disk)
	if rest == node {
		// some other naming: the trailing digits
		i := len(node)
		for i > 0 && node[i-1] >= '0' && node[i-1] <= '9' {
			i--
		}
		rest = node[i:]
	}
	n, err := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(rest, "-part"), "p"))
	if err != nil {
		return 0
	}
	return n
}

// NodeOf is the kernel's name for partition n of a disk.
func NodeOf(disk string, n int) string {
	if last := disk[len(disk)-1]; last >= '0' && last <= '9' {
		return disk + "p" + strconv.Itoa(n)
	}
	return disk + strconv.Itoa(n)
}

// ParseScan reads a scan of several disks: a DiskMarker line per disk, then optionally a HashMarker
// line, then sfdisk's JSON. A disk seen twice (declared and listed) is kept once, in first place.
func ParseScan(out string) ([]Table, error) {
	tables := []Table{}
	seen := map[string]bool{}
	sections := strings.Split(out, DiskMarker)
	for _, section := range sections[1:] {
		path, body, _ := strings.Cut(section, "\n")
		path = strings.TrimSpace(path)
		hash := ""
		if strings.HasPrefix(body, HashMarker) {
			var line string
			line, body, _ = strings.Cut(body, "\n")
			hash = strings.Fields(strings.TrimPrefix(line, HashMarker) + " ")[0]
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		t, err := ParseSfdisk(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		t.Path, t.Hash = path, hash
		if t.Device == "" {
			t.Device = path
		}
		tables = append(tables, t)
	}
	return tables, nil
}

// Found is a partition with the disk it is on.
type Found struct {
	Table     Table
	Partition Partition
}

// Find is the partition with that GPT name. prefer, when not empty, is the resolved path of the
// declared disk: a name found there wins over the same name elsewhere. ok is false when no disk
// has it; more than one candidate and no preference between them is an error, because swapping to
// the wrong one of two same-named partitions is not something to guess at.
func Find(tables []Table, name, prefer string) (Found, bool, error) {
	var all []Found
	for _, t := range tables {
		if t.Label != "gpt" {
			continue
		}
		for _, p := range t.Partitions {
			if p.Name == name {
				all = append(all, Found{t, p})
			}
		}
	}
	if len(all) == 0 {
		return Found{}, false, nil
	}
	if prefer != "" {
		var here []Found
		for _, f := range all {
			if f.Table.Path == prefer {
				here = append(here, f)
			}
		}
		if len(here) == 1 {
			return here[0], true, nil
		}
		if len(here) > 1 {
			return Found{}, false, fmt.Errorf("%s has %d partitions named %s; GPT names must be unique for this resource", prefer, len(here), name)
		}
	}
	if len(all) > 1 {
		where := make([]string, len(all))
		for i, f := range all {
			where[i] = f.Partition.Node
		}
		return Found{}, false, fmt.Errorf("more than one partition is named %s (%s); rename all but one", name, strings.Join(where, ", "))
	}
	return all[0], true, nil
}

// Placement is where a new partition goes: its number and its first and last sectors.
type Placement struct {
	Number int
	Start  int64
	End    int64
}

// Plan places a new partition of sizeGib GiB on a disk, in free space only. With start, the
// partition begins exactly there; without, at the first 1 MiB boundary after the last partition.
// It refuses a disk that is not GPT, a size that is not a whole number of sectors, a region outside
// the usable sectors, and any overlap with an existing partition. It never moves, resizes or
// deletes anything, so every refusal leaves the disk as it was.
func Plan(t Table, sizeGib int, start *int64) (Placement, error) {
	if t.Label != "gpt" {
		if t.Label == "" {
			return Placement{}, fmt.Errorf("%s has no partition table; SwapPartition only adds a partition to an existing GPT and never writes a new table", t.Path)
		}
		return Placement{}, fmt.Errorf("%s has a %s partition table; SwapPartition only adds partitions to GPT", t.Path, t.Label)
	}
	if sizeGib <= 0 {
		return Placement{}, errors.New("sizeGib must be at least 1")
	}
	bytes := int64(sizeGib) << 30
	if bytes%t.SectorSize != 0 {
		return Placement{}, fmt.Errorf("%d GiB is not a whole number of %d-byte sectors", sizeGib, t.SectorSize)
	}
	sectors := bytes / t.SectorSize
	align := int64(1<<20) / t.SectorSize
	if align < 1 {
		align = 1
	}
	parts := append([]Partition{}, t.Partitions...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Start < parts[j].Start })
	first := t.FirstLBA
	if start != nil {
		first = *start
	} else {
		next := t.FirstLBA
		for _, p := range parts {
			if p.End()+1 > next {
				next = p.End() + 1
			}
		}
		first = (next + align - 1) / align * align
	}
	end := first + sectors - 1
	if first < t.FirstLBA {
		return Placement{}, fmt.Errorf("sector %d is before %s's first usable sector, %d", first, t.Path, t.FirstLBA)
	}
	if end > t.LastLBA {
		free := t.LastLBA - first + 1
		if free < 0 {
			free = 0
		}
		return Placement{}, fmt.Errorf("%d GiB starting at sector %d does not fit on %s: %d MiB is free there, up to its last usable sector %d",
			sizeGib, first, t.Path, free*t.SectorSize>>20, t.LastLBA)
	}
	for _, p := range parts {
		if first <= p.End() && p.Start <= end {
			return Placement{}, fmt.Errorf("sectors %d-%d would overlap partition %d (%s, %q, sectors %d-%d); SwapPartition only uses free space",
				first, end, p.Number, p.Node, p.Name, p.Start, p.End())
		}
	}
	used := map[int]bool{}
	for _, p := range parts {
		used[p.Number] = true
	}
	n := 1
	for used[n] {
		n++
	}
	if n > 128 {
		return Placement{}, fmt.Errorf("%s has no free partition entry", t.Path)
	}
	return Placement{Number: n, Start: first, End: end}, nil
}

// Signature is what blkid -p found on a partition: "" for nothing at all, "swap", or the name of
// anything else (a filesystem, a RAID member, a partition table).
type Signature struct {
	Kind  string
	Label string
	UUID  string
}

// ParseBlkid reads `blkid -p -o export <node>` and its exit code: 2 means no signature, 0 one,
// anything else a probe that failed (ambiguous signatures among them), which is never "nothing".
func ParseBlkid(out string, code int) (Signature, error) {
	if code == 2 {
		return Signature{}, nil
	}
	if code != 0 {
		return Signature{}, fmt.Errorf("blkid could not probe the partition (exit %d): %s", code, strings.TrimSpace(out))
	}
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[k] = v
		}
	}
	s := Signature{Kind: values["TYPE"], Label: values["LABEL"], UUID: values["UUID"]}
	if s.Kind == "" && values["PTTYPE"] != "" {
		s.Kind = "partition table " + values["PTTYPE"]
	}
	return s, nil
}

// Active is one swap area the kernel is using.
type Active struct {
	Path     string
	Priority int
	Size     int64
	Used     int64
}

// ParseSwaps reads `swapon --show=NAME,TYPE,SIZE,USED,PRIO --raw --bytes --noheadings`.
func ParseSwaps(out string) []Active {
	found := []Active{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		size, _ := strconv.ParseInt(f[2], 10, 64)
		used, _ := strconv.ParseInt(f[3], 10, 64)
		prio, err := strconv.Atoi(f[4])
		if err != nil {
			continue
		}
		found = append(found, Active{Path: unescapeRaw(f[0]), Priority: prio, Size: size, Used: used})
	}
	return found
}

var rawEscape = regexp.MustCompile(`\\x([0-9a-fA-F]{2})`)

// unescapeRaw undoes the \xNN escapes util-linux's raw output uses for spaces and the like.
func unescapeRaw(s string) string {
	return rawEscape.ReplaceAllStringFunc(s, func(m string) string {
		b, _ := strconv.ParseUint(m[2:], 16, 8)
		return string(rune(b))
	})
}

// LookupActive is the swap area on that node, if the kernel is using it.
func LookupActive(swaps []Active, node string) (Active, bool) {
	for _, a := range swaps {
		if a.Path == node {
			return a, true
		}
	}
	return Active{}, false
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$`)

// ValidName checks a name: it is the GPT name, the swap label (which mkswap caps at 16 bytes) and
// part of a path under /dev/disk/by-partlabel, so it is kept to characters that need no escaping.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("name %q must be 1-16 characters of A-Z, a-z, 0-9, '.', '_' and '-', starting with a letter or digit", name)
	}
	return nil
}

// Discard values and what they mean to swapon and to a swap unit's Options=.
var discards = map[string]struct{ swapon, option string }{
	"once":  {"--discard=once", "discard=once"},
	"pages": {"--discard=pages", "discard=pages"},
	"both":  {"--discard", "discard"},
}

// ValidDiscard checks a discard policy.
func ValidDiscard(d *string) error {
	if d == nil {
		return nil
	}
	if _, ok := discards[*d]; !ok {
		return fmt.Errorf("discard %q must be once, pages or both", *d)
	}
	return nil
}

// ValidPriority checks a swap priority: swapon takes 0 to 32767.
func ValidPriority(p int) error {
	if p < 0 || p > 32767 {
		return fmt.Errorf("priority %d must be between 0 and 32767", p)
	}
	return nil
}

// SwaponArgs is the swapon command line for a node at a priority and discard policy.
func SwaponArgs(priority int, discard *string) string {
	args := "-p " + strconv.Itoa(priority)
	if discard != nil {
		args += " " + discards[*discard].swapon
	}
	return args
}

// What is the path the unit swaps to: the partition by its GPT name, which survives the disks
// being enumerated in another order.
func What(name string) string { return "/dev/disk/by-partlabel/" + name }

// UnitMarker is the first line of every unit this resource writes.
const UnitMarker = "# Managed by pulumi-homelab (SwapPartition). Hand edits show up as drift on the next refresh."

// Unit is the swap unit's content.
func Unit(name string, priority int, discard *string) string {
	var b strings.Builder
	b.WriteString(UnitMarker + "\n")
	b.WriteString("[Unit]\nDescription=Swap on the GPT partition " + name + "\n\n")
	b.WriteString("[Swap]\nWhat=" + What(name) + "\nPriority=" + strconv.Itoa(priority) + "\n")
	if discard != nil {
		b.WriteString("Options=" + discards[*discard].option + "\n")
	}
	b.WriteString("\n[Install]\nWantedBy=swap.target\n")
	return b.String()
}

// UnitSettings reads Priority= and the discard option back out of a unit, for an import.
func UnitSettings(unit string) (priority *int, discard *string) {
	for _, line := range strings.Split(unit, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "Priority":
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				priority = &n
			}
		case "Options":
			for _, o := range strings.Split(v, ",") {
				for d, spelled := range discards {
					if strings.TrimSpace(o) == spelled.option {
						d := d
						discard = &d
					}
				}
			}
		}
	}
	return priority, discard
}

// UnitName is systemd's name for the swap unit of a path: `systemd-escape -p --suffix=swap`.
func UnitName(path string) string {
	parts := []string{}
	for _, p := range strings.Split(path, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "-.swap"
	}
	var b strings.Builder
	for i, part := range parts {
		if i > 0 {
			b.WriteByte('-')
		}
		for j := 0; j < len(part); j++ {
			c := part[j]
			keep := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.'
			if keep && !(c == '.' && i == 0 && j == 0) {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		}
	}
	return b.String() + ".swap"
}

// SwapoffMargin is the memory left over that turning a swap area off must not eat into.
const SwapoffMargin = int64(512) << 20

// SafeToSwapoff refuses to turn off a swap area whose pages would not fit in the memory available
// now: swapoff pulls every one of them back into RAM, and on a machine short of memory that is how
// it stops answering.
func SafeToSwapoff(node string, used, memAvailable int64) error {
	if used+SwapoffMargin > memAvailable {
		return fmt.Errorf("%s holds %d MiB of swapped pages and only %d MiB of memory is available; turning it off now could run the machine out of memory, so it is left on. Free memory, or swapoff it by hand, and run again",
			node, used>>20, memAvailable>>20)
	}
	return nil
}
