package swap

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func eq[T any](t *testing.T, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestParseProcSwaps(t *testing.T) {
	proc := strings.Join([]string{
		"Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority",
		"/var/swap                               file            2097148 0       -2",
		"/dev/mmcblk0p3                          partition       1048572 512     -3",
	}, "\n")
	t.Run("skips the header and reads both kinds", func(t *testing.T) {
		swaps := ParseProcSwaps(proc)
		paths, kinds := []string{}, []string{}
		for _, s := range swaps {
			paths = append(paths, s.Path)
			kinds = append(kinds, s.Kind)
		}
		eq(t, paths, []string{"/var/swap", "/dev/mmcblk0p3"})
		eq(t, kinds, []string{"file", "partition"})
	})
	t.Run("rounds kilobytes to the megabytes the size was asked for in", func(t *testing.T) {
		swaps := ParseProcSwaps(proc)
		eq(t, swaps[0].SizeMB, 2048)
		eq(t, swaps[1].SizeMB, 1024)
	})
	t.Run("reads a negative priority, which is what the kernel gives an ordinary swapfile", func(t *testing.T) {
		eq(t, ParseProcSwaps(proc)[0].Priority, -2.0)
	})
	t.Run("says nothing rather than something wrong on a machine with no swap at all", func(t *testing.T) {
		eq(t, ParseProcSwaps("Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"), []Active{})
		eq(t, ParseProcSwaps(""), []Active{})
	})
	t.Run("splits and trims on JavaScript's whitespace, not Go's", func(t *testing.T) {
		eq(t, ParseProcSwaps("h\n\ufeff/a file 2048 0 -2"), []Active{{Path: "/a", Kind: "file", SizeMB: 2, Priority: -2}})
		eq(t, ParseProcSwaps("h\n/a\u0085file 2048 0 -2"), []Active{{Path: "/a\u0085file", Kind: "2048", SizeMB: 0, Priority: 0}})
		eq(t, ParseProcSwaps("h\n/a\u00a0file\u3000 2048 0 -2"), []Active{{Path: "/a", Kind: "file", SizeMB: 2, Priority: -2}})
	})
	t.Run("reads the priority with Number(), Infinity and radix prefixes included", func(t *testing.T) {
		eq(t, ParseProcSwaps("h\n/a file 0x800 0 Infinity")[0].Priority, math.Inf(1))
		eq(t, ParseProcSwaps("h\n/a file 0x800 0 -Infinity")[0].SizeMB, 2)
		eq(t, math.IsNaN(ParseProcSwaps("h\n/a file 2048 0 inf")[0].Priority), true)
	})
	t.Run("rounds a half up exactly as Math.round does", func(t *testing.T) {
		eq(t, ParseProcSwaps("h\n/a file 1536 0 -2")[0].SizeMB, 2)
		eq(t, ParseProcSwaps("h\n/a file 511.99999999999994 0 -2")[0].SizeMB, 0)
	})
}

func TestParseFstabSwap(t *testing.T) {
	fstab := strings.Join([]string{
		"proc            /proc           proc    defaults          0       0",
		"PARTUUID=00112233-01  /boot/firmware  vfat    defaults          0       2",
		"/var/swap none swap sw 0 0",
	}, "\n")
	t.Run("finds an fstab swap line", func(t *testing.T) {
		eq(t, ParseFstabSwap(fstab), []string{"/var/swap none swap sw 0 0"})
	})
	t.Run("does not read a commented line as one that is in force", func(t *testing.T) {
		eq(t, ParseFstabSwap("#/var/swap none swap sw 0 0"), []string{})
		eq(t, ParseFstabSwap("  # /var/swap none swap sw 0 0"), []string{})
	})
	t.Run("ignores every other kind of mount", func(t *testing.T) {
		eq(t, ParseFstabSwap("PARTUUID=00112233-02  /  ext4  defaults,noatime  0  1"), []string{})
	})
	t.Run("finds a partition as readily as a file", func(t *testing.T) {
		eq(t, len(ParseFstabSwap("UUID=abcd-1234 none swap sw 0 0")), 1)
	})
	t.Run("trims a BOM and keeps a NEL, as String.prototype.trim does", func(t *testing.T) {
		eq(t, ParseFstabSwap("\ufeff/var/swap none swap sw 0 0"), []string{"/var/swap none swap sw 0 0"})
		eq(t, ParseFstabSwap("/var/swap none swap sw 0 0\u0085"), []string{"/var/swap none swap sw 0 0\u0085"})
	})
}

func TestFstabLinesFor(t *testing.T) {
	lines := []string{"/var/swap none swap sw 0 0", "/var/swap2 none swap sw 0 0", "UUID=abcd none swap sw 0 0"}
	t.Run("matches the source field exactly, not as a prefix", func(t *testing.T) {
		eq(t, FstabLinesFor(lines, "/var/swap"), []string{"/var/swap none swap sw 0 0"})
	})
	t.Run("finds nothing for a path fstab does not mention", func(t *testing.T) {
		eq(t, FstabLinesFor(lines, "/swapfile"), []string{})
	})
	t.Run("ends the source field at any JavaScript whitespace", func(t *testing.T) {
		for _, line := range []string{"/var/swap\u00a0none swap sw 0 0", "/var/swap\u2028none swap sw 0 0", "/var/swap\u3000none"} {
			eq(t, FstabLinesFor([]string{line}, "/var/swap"), []string{line})
		}
		eq(t, FstabLinesFor([]string{"/var/swap\u0085none"}, "/var/swap"), []string{})
	})
}
