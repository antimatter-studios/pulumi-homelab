//go:build integration

package homelab

import (
	"os/exec"
	"strings"
	"sync"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/swappart"
)

// SwapPartition against a real Debian 13 with real gdisk, fdisk and util-linux, unprivileged: the
// disk is a 3 GiB sparse image file holding an EFI partition and a root partition, and
// testdata/debian13-disk's scripts stand in for the kernel, udev, swapon and systemd. So the
// partitioning, probing and formatting are real, and nothing can reach a real disk or real swap.
const diskImage = "pulumi-homelab-disk:debian13"

func onDisk(t *testing.T) (lifecycleServer, func(string) string) {
	t.Helper()
	if out, err := exec.Command("docker", "build", "-q", "-t", diskImage, "testdata/debian13-disk").CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", diskImage).Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	root := func(command string) string {
		out, err := exec.Command("docker", "exec", id, "sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", command, err, out)
		}
		return string(out)
	}
	root("truncate -s 3G /disk.img && sgdisk -n 1:2048:+32M -t 1:ef00 -c 1:EFI -n 2:0:+64M -t 2:8300 -c 2:root /disk.img >/dev/null && " +
		"partx -a --nr 2 /disk.img && mke2fs -q -t ext4 -F /disk.img2")
	return serve(t, dockerRunner{id}), root
}

// the partition lines of the image's table, which every test compares before and after
func partitions(root func(string) string) string {
	return root("sfdisk --dump /disk.img | grep '^/disk.img'")
}

func partLine(dump string, n int) string {
	for _, l := range strings.Split(dump, "\n") {
		if strings.HasPrefix(l, "/disk.img"+itoa(n)+" ") {
			return l
		}
	}
	return ""
}

func TestSwapPartitionLifecycleOnDebian13(t *testing.T) {
	s, root := onDisk(t)
	before := partitions(root)
	unitFile := "/etc/systemd/system/" + swappart.UnitName(swappart.What("swap-test"))
	cycle(t, s, "SwapPartition",
		m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5),
		m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 7, "discard", "once"),
		func(stage string, state property.Map) {
			after := partitions(root)
			// the EFI and root partitions are exactly as they were, and root still holds ext4
			if partLine(after, 1) != partLine(before, 1) || partLine(after, 2) != partLine(before, 2) {
				t.Fatalf("%s: other partitions changed:\n%s\n%s", stage, before, after)
			}
			expect(t, root, "blkid -p -o value -s TYPE /disk.img2", "ext4\n")
			// root ends at sector 198655; the next 1 MiB boundary is 198656
			p3 := partLine(after, 3)
			if !strings.Contains(p3, "start=      198656, size=     2097152, type="+swappart.SwapType) || !strings.Contains(p3, `name="swap-test"`) {
				t.Fatalf("%s: %q", stage, p3)
			}
			switch stage {
			case "create":
				expect(t, root, "blkid -p -o value -s TYPE /disk.img3; blkid -p -o value -s LABEL /disk.img3", "swap\nswap-test\n")
				expect(t, root, "cat /run/swaps", "/disk.img3 partition 1073741824 0 5\n")
				expect(t, root, "cat '"+unitFile+"'", swappart.Unit("swap-test", 5, nil))
				expect(t, root, "test -L '/etc/systemd/system/swap.target.wants/"+swappart.UnitName(swappart.What("swap-test"))+"' && echo enabled", "enabled\n")
				if state.Get("device").AsString() != "/disk.img3" || state.Get("number").AsNumber() != 3 || !state.Get("active").AsBool() {
					t.Fatalf("%v", state)
				}
			case "update":
				expect(t, root, "cat /run/swaps", "/disk.img3 partition 1073741824 0 7\n")
				expect(t, root, "cat '"+unitFile+"'", swappart.Unit("swap-test", 7, sp("once")))
			case "delete":
				// swap off, unit gone, the partition and its signature kept
				expect(t, root, "cat /run/swaps; ls /etc/systemd/system/swap.target.wants; test -e '"+unitFile+"' || echo no-unit", "no-unit\n")
				expect(t, root, "blkid -p -o value -s LABEL /disk.img3", "swap-test\n")
			}
		})
}

func TestSwapPartitionImportOnDebian13(t *testing.T) {
	s, root := onDisk(t)
	inputs := m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5)
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("SwapPartition", "made"), Properties: inputs}); err != nil {
		t.Fatal(err)
	}
	calls := root("grep -vc '^swapon --show' /tmp/calls.log")
	// an image is not a disk lsblk lists, so the import id names it
	read, err := s.Read(p.ReadRequest{ID: "/disk.img:swap-test", Urn: urnOf("SwapPartition", "imported")})
	if err != nil || read.ID != "swap-test" {
		t.Fatalf("%+v %v", read, err)
	}
	if d, err := s.Diff(p.DiffRequest{ID: "swap-test", Urn: urnOf("SwapPartition", "imported"), State: read.Properties, Inputs: inputs}); err != nil || d.HasChanges {
		t.Fatalf("import shows a change: %+v %v\n%v", d, err, read.Inputs)
	}
	// an import only reads: no shim was called but swapon --show
	expect(t, root, "grep -vc '^swapon --show' /tmp/calls.log", calls)
}

func TestSwapPartitionRefusesOnDebian13(t *testing.T) {
	s, root := onDisk(t)
	before := partitions(root)
	urn := urnOf("SwapPartition", "it")
	create := func(inputs property.Map, want string) {
		t.Helper()
		if _, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%v: %v", inputs, err)
		}
		if after := partitions(root); after != before {
			t.Fatalf("a refusal changed the disk:\n%s\n%s", before, after)
		}
	}
	create(m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5, "start", 100000), `would overlap partition 2 (/disk.img2, "root"`)
	create(m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5, "start", 2048), `would overlap partition 1 (/disk.img1, "EFI"`)
	create(m("disk", "/disk.img", "name", "swap-test", "sizeGib", 3, "priority", 5), "does not fit on /disk.img")
	create(m("disk", "/disk.img", "name", "root", "sizeGib", 1, "priority", 5), "never resizes a partition")
	root("truncate -s 2G /plain.img")
	create(m("disk", "/plain.img", "name", "swap-test", "sizeGib", 1, "priority", 5), "has no partition table")
	expect(t, root, "blkid -p /plain.img || echo blank", "blank\n")

	// a partition already named and typed as swap, holding a filesystem: never formatted
	root("sgdisk -n 3:0:+1G -t 3:8200 -c 3:swap-ext /disk.img >/dev/null && partx -a --nr 3 /disk.img && mke2fs -q -t ext4 -F /disk.img3")
	before = partitions(root)
	create(m("disk", "/disk.img", "name", "swap-ext", "sizeGib", 1, "priority", 5), "holds a ext4 signature")
	expect(t, root, "blkid -p -o value -s TYPE /disk.img3; grep -c 'swapon -p\\|systemctl' /tmp/calls.log || true", "ext4\n0\n")
}

// deletePartition removes the partition it made, by number, and nothing else.
func TestSwapPartitionDeletePartitionOnDebian13(t *testing.T) {
	s, root := onDisk(t)
	before := partitions(root)
	urn := urnOf("SwapPartition", "it")
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5, "deletePartition", true)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if after := partitions(root); after != before {
		t.Fatalf("\n%s\n%s", before, after)
	}
	expect(t, root, "cat /run/swaps; test -e /disk.img3 || echo gone", "gone\n")

	// the table changed under it between the read and the lock: the entry at that number has another
	// GPT id, and it is left alone
	created, err = s.Create(p.CreateRequest{Urn: urn, Properties: m("disk", "/disk.img", "name", "swap-test", "sizeGib", 1, "priority", 5, "deletePartition", true)})
	if err != nil {
		t.Fatal(err)
	}
	uuid := created.Properties.Get("partUuid").AsString()
	root("sgdisk -u 3:R /disk.img >/dev/null")
	changed := partitions(root)
	if out := root("sh -c " + shellQuote(deleteSwapPartCommand("/disk.img", 3, "/disk.img3", "swap-test", uuid)) + " 2>&1 || echo refused"); !strings.Contains(out, "not the recorded one; it is left alone\nrefused") {
		t.Fatalf("%q", out)
	}
	expect(t, root, "blkid -p -o value -s LABEL /disk.img3", "swap-test\n")
	if after := partitions(root); after != changed {
		t.Fatalf("\n%s\n%s", changed, after)
	}
}

// Two partitions made on one disk at once: the disk's lock and the table check mean they never
// claim the same free space. Each either succeeds or refuses, and what succeeded does not overlap.
func TestSwapPartitionsSideBySideOnDebian13(t *testing.T) {
	s, root := onDisk(t)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, name := range []string{"swap-a", "swap-b"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			_, errs[i] = s.Create(p.CreateRequest{Urn: urnOf("SwapPartition", name), Properties: m("disk", "/disk.img", "name", name, "sizeGib", 1, "priority", 5)})
		}(i, name)
	}
	wg.Wait()
	made := 0
	for _, err := range errs {
		if err == nil {
			made++
		} else if !strings.Contains(err.Error(), "changed since it was read") {
			t.Fatalf("%v", err)
		}
	}
	dump := partitions(root)
	if made == 0 || strings.Count(dump, "type="+swappart.SwapType) != made {
		t.Fatalf("%d made:\n%s", made, dump)
	}
	expect(t, root, "sgdisk -v /disk.img | grep -c 'No problems found'", "1\n")
}
