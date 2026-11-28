//go:build integration

package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Exercise the lifecycle of the system resources that can run in the Debian SSH fixture.

func expect(t *testing.T, root func(string) string, command, want string) {
	t.Helper()
	if got := root(command); got != want {
		t.Fatalf("%s:\ngot  %q\nwant %q", command, got, want)
	}
}

func TestPreconditionLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("touch /root/flag")
	cycle(t, s, "Precondition",
		m("check", "test -f /root/flag", "message", "create /root/flag", "root", true),
		m("check", "test -f /root/flag", "message", "create /root/flag and deploy again", "root", true),
		func(stage string, state property.Map) {
			if stage == "delete" {
				root("test -f /root/flag")
			}
		})
}

func TestUserLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("groupadd media")
	cycle(t, s, "User",
		m("name", "svc", "shell", "/usr/sbin/nologin", "uid", 995),
		m("name", "svc", "shell", "/bin/sh", "uid", 995, "groups", []string{"media"}),
		func(stage string, state property.Map) {
			switch stage {
			case "create":
				expect(t, root, "getent passwd svc | cut -d: -f3,6,7", "995:/home/svc:/usr/sbin/nologin\n")
				root("echo world > /home/svc/data")
			case "update":
				expect(t, root, "getent passwd svc | cut -d: -f7; id -nG svc", "/bin/sh\nsvc media\n")
			case "delete":
				expect(t, root, "getent passwd svc || echo gone; cat /home/svc/data", "gone\nworld\n")
			}
		})
}

func TestSudoRuleLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	rule := func(commands ...string) property.Map {
		return m("user", "chris", "passwordless", true, "commands", commands, "file", "backup", "directory", "/etc/sudoers.d")
	}
	cycle(t, s, "SudoRule", rule("/usr/bin/true"), rule("/usr/bin/true", "/usr/bin/false"), func(stage string, state property.Map) {
		switch stage {
		case "create":
			expect(t, root, "stat -c '%a %U' /etc/sudoers.d/backup; tail -1 /etc/sudoers.d/backup", "440 root\nchris ALL=(ALL) NOPASSWD: /usr/bin/true\n")
		case "update":
			expect(t, root, "tail -1 /etc/sudoers.d/backup", "chris ALL=(ALL) NOPASSWD: /usr/bin/true, /usr/bin/false\n")
		case "delete":
			expect(t, root, "ls /etc/sudoers.d", "README\nchris\n")
		}
	})
}

func TestAuthorizedKeyLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIotherotherotherother other"
	root("echo '" + other + "' > /home/chris/.ssh/authorized_keys && chown chris /home/chris/.ssh/authorized_keys && chmod 0644 /home/chris/.ssh/authorized_keys")
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnopqrstuv laptop"
	cycle(t, s, "AuthorizedKey", m("user", "chris", "key", key), m("user", "chris", "key", key, "options", []string{"restrict"}),
		func(stage string, state property.Map) {
			switch stage {
			case "create":
				expect(t, root, "stat -c '%a %U' /home/chris/.ssh /home/chris/.ssh/authorized_keys; cat /home/chris/.ssh/authorized_keys",
					"700 chris\n600 chris\n"+other+"\n"+key+"\n")
			case "update":
				expect(t, root, "tail -1 /home/chris/.ssh/authorized_keys", "restrict "+key+"\n")
			case "delete":
				expect(t, root, "cat /home/chris/.ssh/authorized_keys", other+"\n")
			}
		})
}

func TestSshKeyLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	key := func(allow bool) property.Map {
		return m("path", "/home/chris/.ssh/id_deploy", "comment", "deploy", "owner", "chris", "group", "chris", "allowDelete", allow)
	}
	cycle(t, s, "SshKey", key(false), key(true), func(stage string, state property.Map) {
		switch stage {
		case "create":
			expect(t, root, "stat -c '%a %U %G' /home/chris/.ssh/id_deploy /home/chris/.ssh/id_deploy.pub", "600 chris chris\n644 chris chris\n")
			if !strings.HasPrefix(state.Get("fingerprint").AsString(), "SHA256:") {
				t.Fatalf("%v", state)
			}
		case "delete":
			expect(t, root, "ls /home/chris/.ssh/id_deploy* 2>/dev/null || echo gone", "gone\n")
		}
	})
}

func TestManagedLineLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	before := root("cat /home/chris/.bashrc")
	line := func(text string) property.Map {
		return m("path", "/home/chris/.bashrc", "line", text, "marker", "shims", "position", "before", "anchor", "case $- in")
	}
	cycle(t, s, "ManagedLine", line(`export PATH="$HOME/x:$PATH"`), line(`export PATH="$HOME/y:$PATH"`), func(stage string, state property.Map) {
		switch stage {
		case "create", "update":
			if !strings.Contains(root("grep -n -B0 'pulumi-homelab:shims' /home/chris/.bashrc"), state.Get("line").AsString()) {
				t.Fatalf("%s: %v", stage, state)
			}
			root("awk '/pulumi-homelab:shims/ {l = NR} /case \\$- in/ {a = NR} END {exit !(l < a)}' /home/chris/.bashrc")
		case "delete":
			expect(t, root, "cat /home/chris/.bashrc", before)
		}
	})
}

func TestKernelCmdlineLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("mkdir -p /boot/firmware && echo 'console=tty1  root=PARTUUID=x rootwait' > /boot/firmware/cmdline.txt")
	cycle(t, s, "KernelCmdline", m("flags", []string{"cgroup_memory=1"}), m("flags", []string{"cgroup_memory=1", "cgroup_enable=memory"}),
		func(stage string, state property.Map) {
			switch stage {
			case "create":
				expect(t, root, "cat /boot/firmware/cmdline.txt", "console=tty1 root=PARTUUID=x rootwait cgroup_memory=1\n")
			case "update", "delete":
				// delete never takes parameters off the boot line
				expect(t, root, "cat /boot/firmware/cmdline.txt", "console=tty1 root=PARTUUID=x rootwait cgroup_memory=1 cgroup_enable=memory\n")
			}
		})
}

func TestFstabEntryLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("printf '# static\\nPARTUUID=1-02 / ext4 defaults 0 1\\n' > /etc/fstab.test")
	before := root("cat /etc/fstab.test")
	entry := func(options ...string) property.Map {
		return m("source", "UUID=abcd", "target", "/mnt/data", "type", "ext4", "options", options, "file", "/etc/fstab.test")
	}
	cycle(t, s, "FstabEntry", entry("defaults"), entry("defaults", "nofail"), func(stage string, state property.Map) {
		switch stage {
		case "create":
			expect(t, root, "cat /etc/fstab.test", before+"# pulumi-homelab\nUUID=abcd /mnt/data ext4 defaults 0 0\n")
		case "update":
			expect(t, root, "cat /etc/fstab.test", before+"# pulumi-homelab\nUUID=abcd /mnt/data ext4 defaults,nofail 0 0\n")
		case "delete":
			expect(t, root, "cat /etc/fstab.test", before)
		}
	})
}

// Somebody edits the entry and a refresh sees it: delete keeps it.
func TestBootConfigLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("mkdir -p /boot/firmware/overlays && touch /boot/firmware/overlays/dwc2.dtbo && " +
		"printf 'dtparam=audio=on\\narm_boost=1\\n\\n[pi5]\\n# fast\\narm_freq=2400\\n' > /boot/firmware/config.txt")
	before := root("cat /boot/firmware/config.txt")
	cycle(t, s, "BootConfig",
		m("section", "pi5", "settings", m("arm_boost", true), "overlays", []string{"dtoverlay=dwc2,dr_mode=host"}),
		m("section", "pi5", "settings", m("arm_boost", true, "gpu_mem", 128), "overlays", []string{"dtoverlay=dwc2,dr_mode=host"}),
		func(stage string, state property.Map) {
			switch stage {
			case "create":
				expect(t, root, "cat /boot/firmware/config.txt", "dtparam=audio=on\narm_boost=1\n\n[pi5]\n# fast\narm_freq=2400\n\narm_boost=1\ndtoverlay=dwc2,dr_mode=host\n")
			case "update":
				// another blank line before the appended setting: the section body keeps the file's
				// trailing empty line and appends after it, as the TypeScript's applyToSection does
				expect(t, root, "sed -n '/\\[pi5\\]/,$p' /boot/firmware/config.txt", "[pi5]\n# fast\narm_freq=2400\n\narm_boost=1\ndtoverlay=dwc2,dr_mode=host\n\ngpu_mem=128\n")
			case "delete":
				// the [all] arm_boost=1 is not this resource's and stays; the blank line is the TypeScript's
				expect(t, root, "cat /boot/firmware/config.txt", strings.TrimSuffix(before, "\n")+"\n\n\n")
			}
		})
}

func TestSshdConfigLifecycleOnDebian13(t *testing.T) {
	s, root := onRunningSshd(t)
	drop := func(interval float64) property.Map {
		return m("file", "90-tunnel", "directory", sshdDirectory, "config", sshdConfig,
			"settings", m("allowTcpForwarding", "remote", "clientAliveInterval", interval), "unchecked", map[string]string{"PermitTTY": "no"}, "match", m("User", "tunnel"))
	}
	head := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nMatch User tunnel\n"
	cycle(t, s, "SshdConfig", drop(15), drop(30), func(stage string, state property.Map) {
		switch stage {
		case "create":
			expect(t, root, "cat /etc/ssh/sshd_config.d/90-tunnel.conf", head+"    AllowTcpForwarding remote\n    ClientAliveInterval 15\n    PermitTTY no\n")
		case "update":
			expect(t, root, "cat /etc/ssh/sshd_config.d/90-tunnel.conf; sshd -t && echo valid", head+"    AllowTcpForwarding remote\n    ClientAliveInterval 30\n    PermitTTY no\nvalid\n")
		case "delete":
			expect(t, root, "ls /etc/ssh/sshd_config.d | grep -c tunnel || true", "0\n")
		}
	})
}

// Swap against a container: it cannot switch real swap on (and must not touch the host's, which
// /proc/swaps shows), so the life is the disable path scoped to its own path with otherSwap allow.
// Swap has no in-place update a container can make safely, so it is create, refresh, delete.
func TestSwapLifecycleOnDebian13(t *testing.T) {
	s, root := onDebian(t)
	root("printf 'PARTUUID=1-02 / ext4 defaults 0 1\\n/var/swap none swap sw 0 0\\n/other none swap sw 0 0\\n' > /etc/fstab")
	urn := urnOf("Swap", "it")
	inputs := m("enabled", false, "otherSwap", "allow")
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	// only the declared path is commented out; the other swap is left alone
	want := "PARTUUID=1-02 / ext4 defaults 0 1\n#/var/swap none swap sw 0 0\n/other none swap sw 0 0\n"
	expect(t, root, "cat /etc/fstab", want)
	refreshed := refreshNoChanges(t, s, urn, created.ID, created.Properties, inputs, "after create")
	// an update scoped to another, disposable fstab: its line for the path is commented out, the
	// first file is not touched again
	root("printf '/var/swap none swap sw 0 0\\n/other none swap sw 0 0\\n' > /etc/fstab.b")
	moved := m("enabled", false, "otherSwap", "allow", "fstab", "/etc/fstab.b")
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: urn, State: refreshed, Inputs: moved})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, root, "cat /etc/fstab.b /etc/fstab", "#/var/swap none swap sw 0 0\n/other none swap sw 0 0\n"+want)
	refreshed = refreshNoChanges(t, s, urn, created.ID, updated.Properties, moved, "after update")
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: urn, Properties: refreshed}); err != nil {
		t.Fatal(err)
	}
	expect(t, root, "cat /etc/fstab", want)
}
