package homelab

import (
	"os"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/journald"
	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// Imports of the remaining system resources recover inputs from the machine.

func TestJournaldImport(t *testing.T) {
	content := journald.JournaldFile(map[string]string{"Storage": "volatile", "SystemMaxUse": "200M"})
	fake := remotetest.New()
	fake.When(contains2("90-logs.conf"), reply(content+"#pulumi-homelab#effective\n[Journal]\nStorage=volatile\n#pulumi-homelab#persistent\nabsent\n", 0), 0)
	fake.When(contains2("hand.conf"), reply("[Journal]\nStorage=auto\n#pulumi-homelab#effective\n#pulumi-homelab#persistent\nabsent\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "Journald", "90-logs")
	if read.Inputs.Get("settings").AsMap().Get("SystemMaxUse").AsString() != "200M" {
		t.Errorf("%v", read.Inputs)
	}
	hand, err := s.Read(p.ReadRequest{ID: "hand", Urn: urnOf("Journald", "imported")})
	if err != nil || hand.ID != "hand" {
		t.Fatalf("read of hand-written drop-in: %+v %v", hand, err)
	}
}

func TestHostnameImport(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("hostnamectl --static"), reply("chrispi\n#pulumi-homelab#now\nchrispi\n#pulumi-homelab#hosts\n127.0.0.1\tlocalhost\n127.0.1.1\tchrispi.lan chrispi\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "Hostname", "/etc/hosts")
	if read.Inputs.Get("name").AsString() != "chrispi" || read.Inputs.Get("domain").AsString() != "lan" {
		t.Errorf("%v", read.Inputs)
	}
	// delete leaves the name alone: nothing is sent
	before := len(fake.Commands())
	if err := s.Delete(p.DeleteRequest{ID: "/etc/hosts", Urn: urnOf("Hostname", "h"), Properties: read.Properties}); err != nil || len(fake.Commands()) != before {
		t.Fatalf("%v %v", err, fake.Commands()[before:])
	}
}

const fstabText = "# /etc/fstab\nPARTUUID=1-02 / ext4 defaults,noatime 0 1\n# pulumi-homelab\nUUID=abcd /mnt/storage btrfs defaults,nofail 0 0\n"

func TestFstabEntryImportAndDelete(t *testing.T) {
	var wrote string
	text := fstabText
	fake := remotetest.New()
	fake.When(contains2("cat > "), func(c string) remotetest.Reply { wrote = c; return remotetest.Reply{} }, 0)
	fake.When(contains2("findmnt"), func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: text + "#pulumi-homelab#mounted\n/dev/sda1 btrfs rw\n"}
	}, 0)
	fake.When(contains2("cat '\\''/etc/fstab'\\''"), func(string) remotetest.Reply { return remotetest.Reply{Stdout: text} }, 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "FstabEntry", "/mnt/storage")
	if read.Inputs.Get("source").AsString() != "UUID=abcd" || read.Inputs.Get("options").AsArray().Len() != 2 {
		t.Errorf("%v", read.Inputs)
	}
	// Delete removes the current marked entry even if its contents changed.
	text = strings.Replace(fstabText, "UUID=abcd", "UUID=other", 1)
	if err := s.Delete(p.DeleteRequest{ID: "/mnt/storage", Urn: urnOf("FstabEntry", "e"), Properties: read.Properties}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrote, "/mnt/storage") || strings.Contains(wrote, "# pulumi-homelab") || !strings.Contains(wrote, "PARTUUID=1-02 / ext4") {
		t.Fatalf("%q", wrote)
	}
}

func TestSwapImport(t *testing.T) {
	fake := remotetest.New()
	fake.When(contains2("/proc/swaps"), reply("Filename Type Size Used Priority\n/var/swap file 2097148 0 -2\n#pulumi-homelab#fstab\n/var/swap none swap sw 0 0\n"+
		"#pulumi-homelab#dphys\nmasked\n#pulumi-homelab#unit\ndphys-swapfile.service masked enabled\n#pulumi-homelab#zram\nno\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "Swap", "/var/swap")
	if !read.Inputs.Get("enabled").AsBool() || read.Inputs.Get("sizeMb").AsNumber() != 2048 {
		t.Errorf("%v", read.Inputs)
	}
	// delete only unmasks the unit
	fake.When(contains2("systemctl unmask"), reply("", 0), 0)
	if err := s.Delete(p.DeleteRequest{ID: "/var/swap", Urn: urnOf("Swap", "s"), Properties: read.Properties}); err != nil {
		t.Fatal(err)
	}
	last := fake.Commands()[len(fake.Commands())-1]
	if !strings.Contains(last, "systemctl unmask") || strings.Contains(last, "rm ") || strings.Contains(last, "swapoff") {
		t.Fatalf("%s", last)
	}
}

func TestSshdConfigImportAndIncludeRefusal(t *testing.T) {
	dropIn := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nMatch User tunnel\n    AllowTcpForwarding remote\n    PermitTTY no\n"
	fake := remotetest.New()
	fake.When(contains2("90-tunnel.conf"), reply("Include /etc/ssh/sshd_config.d/*.conf\n#pulumi-homelab#drop\n"+dropIn+"#pulumi-homelab#effective\nallowtcpforwarding yes\n", 0), 0)
	fake.When(contains2("90-new.conf"), reply("PasswordAuthentication no\n#pulumi-homelab#drop\n#pulumi-homelab#effective\n", 0), 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "SshdConfig", "90-tunnel")
	if read.Inputs.Get("match").AsMap().Get("User").AsString() != "tunnel" || read.Inputs.Get("unchecked").AsMap().Get("PermitTTY").AsString() != "no" {
		t.Errorf("%v", read.Inputs)
	}
	if got := read.Properties.Get("overridden").AsArray(); got.Len() != 1 || got.Get(0).AsString() != "AllowTcpForwarding" {
		t.Errorf("overridden %v", got)
	}
	inputs := property.NewMap(map[string]property.Value{"file": property.New("90-new"), "directory": property.New(sshdDirectory), "config": property.New(sshdConfig),
		"settings": property.New(map[string]property.Value{"passwordAuthentication": property.New(false)})})
	if _, err := s.Create(p.CreateRequest{Urn: urnOf("SshdConfig", "new"), Properties: inputs}); err == nil || !strings.Contains(err.Error(), "does not include") || fake.Ran("mode=install") {
		t.Fatalf("%v", err)
	}
}

func TestBootConfigImportAndDelete(t *testing.T) {
	text := "dtparam=audio=on\n\n[pi5]\n# fast\narm_freq=2400\ndtoverlay=dwc2,dr_mode=host\n"
	var wrote string
	fake := remotetest.New()
	fake.When(contains2("cat > "), func(c string) remotetest.Reply { wrote = c; return remotetest.Reply{} }, 0)
	fake.When(contains2("exit 9; }"), func(string) remotetest.Reply { return remotetest.Reply{Stdout: "/boot/firmware/config.txt\n" + text} }, 0)
	fake.When(contains2("vcgencmd"), reply("arm_freq=2400\n", 0), 0)
	fake.When(contains2("cat '\\''/boot/firmware/config.txt'\\''"), func(string) remotetest.Reply { return remotetest.Reply{Stdout: text} }, 0)
	s := configured(t, fake)
	read, _ := importAndDiff(t, s, "BootConfig", "/boot/firmware/config.txt#pi5")
	if read.Inputs.Get("unchecked").AsMap().Get("arm_freq").AsString() != "2400" || read.Inputs.Get("overlays").AsArray().Len() != 1 {
		t.Errorf("%v", read.Inputs)
	}
	text = strings.Replace(text, "arm_freq=2400", "arm_freq=2000", 1)
	if err := s.Delete(p.DeleteRequest{ID: read.ID, Urn: urnOf("BootConfig", "b"), Properties: read.Properties}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrote, "dtparam=audio=on\n\n[pi5]\n# fast\n") || strings.Contains(wrote, "arm_freq") || strings.Contains(wrote, "dwc2") {
		t.Fatalf("%q", wrote)
	}
}

func TestHostnameWritesAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	hostFile, nameFile, restartLog := dir+"/hosts", dir+"/hostname", dir+"/restarts"
	const before = "127.0.0.1\tlocalhost\n127.0.1.1\told old.lan\n192.0.2.10\tother\n"
	for path, content := range map[string]string{hostFile: before, nameFile: "old\n"} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := shellStepRunner(t, map[string]string{
		"cat":      "",
		"hostname": "cat " + shellQuote(nameFile),
		"hostnamectl": "case \"$1\" in\n" +
			" --static) cat " + shellQuote(nameFile) + ";;\n" +
			" set-hostname) printf '%s\\n' \"$2\" > " + shellQuote(nameFile) + ";;\n esac",
		"systemctl": "printf '%s\\n' \"$*\" >> " + shellQuote(restartLog),
	})
	s := configured(t, runner)
	urn := urnOf("Hostname", "test")
	inputs := property.NewMap(map[string]property.Value{
		"name": property.New("new"), "domain": property.New("lan"), "hosts": property.New(hostFile),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil || created.ID != hostFile {
		t.Fatalf("create: %+v, %v", created, err)
	}

	assertFile := func(path, want string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, %v, want %q", path, got, err, want)
		}
	}
	assertFile(nameFile, "new\n")
	assertFile(hostFile, "127.0.0.1\tlocalhost\n127.0.1.1\tnew.lan new\n192.0.2.10\tother\n")
	assertFile(restartLog, "try-restart avahi-daemon\n")

	newInputs := inputs.Set("name", property.New("updated")).Set("restartAvahi", property.New(false))
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, Inputs: newInputs})
	if err != nil || !diff.HasChanges || diff.DeleteBeforeReplace {
		t.Fatalf("rename diff: %+v, %v", diff, err)
	}
	for field, change := range diff.DetailedDiff {
		if change.Kind == p.UpdateReplace || change.Kind == p.AddReplace || change.Kind == p.DeleteReplace {
			t.Fatalf("rename replaces %s: %+v", field, change)
		}
	}
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, Inputs: newInputs})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(nameFile, "updated\n")
	assertFile(hostFile, "127.0.0.1\tlocalhost\n127.0.1.1\tupdated.lan updated\n192.0.2.10\tother\n")
	assertFile(restartLog, "try-restart avahi-daemon\n")

	read, err := s.Read(p.ReadRequest{Urn: urn, ID: created.ID, Properties: updated.Properties, Inputs: newInputs})
	if err != nil || read.ID != created.ID {
		t.Fatalf("refresh: %+v, %v", read, err)
	}
	if diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: read.ID, State: read.Properties, Inputs: newInputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after refresh: %+v, %v", diff, err)
	}
}
