package homelab

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

const failedLookup = "echo test-secret; echo test-secret >&2; exit 17"

func requirePrivateCommandError(t *testing.T, err error, command, status string) {
	t.Helper()
	requireStepError(t, err, command)
	requireStepError(t, err, "(exit "+status+")")
	if strings.Contains(err.Error(), "test-secret") {
		t.Fatal("command failure disclosed remote output")
	}
}

func writeProbeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAptPackageReadFailureAndAbsence(t *testing.T) {
	req := p.ReadRequest{ID: "demo", Urn: urnOf("AptPackage", "demo")}
	t.Run("manual lookup fails", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{
			"dpkg-query": "echo 'installed 1'", "apt-mark": failedLookup,
		})
		_, err := configured(t, runner).Read(req)
		requirePrivateCommandError(t, err, "apt-mark showmanual", "17")
	})
	t.Run("package lookup fails", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{"dpkg-query": "echo test-secret >&2; exit 2"})
		_, err := configured(t, runner).Read(req)
		requirePrivateCommandError(t, err, "dpkg-query", "2")
	})
	t.Run("package is absent", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{"dpkg-query": "exit 1"})
		read, err := configured(t, runner).Read(req)
		if err != nil || read.ID != "" || runner.Ran("apt-mark") {
			t.Fatalf("absent package returned ID %q, %v", read.ID, err)
		}
	})
}

func TestAptPackagesReadFailureAndAbsence(t *testing.T) {
	req := p.ReadRequest{ID: "apt-packages", Urn: urnOf("AptPackages", "demo"), Inputs: props(map[string]property.Value{
		"present": property.New([]property.Value{property.New("known"), property.New("missing")}),
	})}
	t.Run("manual lookup fails", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{
			"dpkg-query": "echo 'known installed 1'; exit 1", "apt-mark": failedLookup,
		})
		_, err := configured(t, runner).Read(req)
		requirePrivateCommandError(t, err, "apt-mark showmanual", "17")
	})
	t.Run("package lookup fails", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{"dpkg-query": "echo test-secret >&2; exit 2"})
		_, err := configured(t, runner).Read(req)
		requirePrivateCommandError(t, err, "dpkg-query", "2")
	})
	t.Run("missing package remains drift", func(t *testing.T) {
		runner := shellStepRunner(t, map[string]string{
			"dpkg-query": "echo 'known installed 1'; exit 1", "apt-mark": "echo known",
		})
		read, err := configured(t, runner).Read(req)
		if err != nil || read.ID != req.ID {
			t.Fatalf("partial package list returned ID %q, %v", read.ID, err)
		}
		missing := read.Properties.Get("missing").AsArray()
		if missing.Len() != 1 || missing.Get(0).AsString() != "missing" {
			t.Fatalf("missing packages: %v", missing)
		}
	})
}

func TestGitCheckoutReadFailureAndAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, script, failed string
		present              bool
	}{
		{"HEAD lookup fails", failedLookup, "git rev-parse HEAD", true},
		{"origin lookup fails", "if [ \"$3\" = remote ]; then " + failedLookup + "; fi; echo " + gitSHA, "git remote get-url origin", true},
		{"origin is absent", "if [ \"$3\" = remote ]; then exit 2; fi; echo " + gitSHA, "", true},
		{"checkout is absent", failedLookup, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir()
			if tc.present {
				if err := os.Mkdir(filepath.Join(path, ".git"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			runner := shellStepRunner(t, map[string]string{"git": tc.script})
			read, err := configured(t, runner).Read(p.ReadRequest{ID: path, Urn: urnOf("GitCheckout", "demo")})
			if tc.failed != "" {
				requirePrivateCommandError(t, err, tc.failed, "17")
				return
			}
			if err != nil || (read.ID == path) != tc.present {
				t.Fatalf("checkout returned ID %q, %v", read.ID, err)
			}
		})
	}
}

func TestSshdConfigReadFailureAndAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, cat, failed string
		present           bool
	}{
		{"config read fails", failedLookup, "cat", true},
		{"effective lookup fails", "", "sshd -T", true},
		{"drop-in is absent", failedLookup, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "sshd_config")
			writeProbeFile(t, config, "Include "+dir+"/*.conf\n")
			if tc.present {
				writeProbeFile(t, filepath.Join(dir, "demo.conf"), "PasswordAuthentication no\n")
			}
			runner := shellStepRunner(t, map[string]string{"cat": tc.cat, "sshd": failedLookup})
			read, err := configured(t, runner).Read(p.ReadRequest{ID: "demo", Urn: urnOf("SshdConfig", "demo"), Properties: props(map[string]property.Value{
				"file": property.New("demo"), "directory": property.New(dir), "config": property.New(config),
			})})
			if tc.failed != "" {
				requirePrivateCommandError(t, err, tc.failed, "17")
			} else if err != nil || read.ID != "" {
				t.Fatalf("absent drop-in returned ID %q, %v", read.ID, err)
			}
		})
	}
}

func TestSshKeyReadFailureAndAbsence(t *testing.T) {
	for _, step := range []string{"cat", "ssh-keygen", "stat", "absent"} {
		t.Run(step, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "id")
			commands := map[string]string{"cat": "", "ssh-keygen": "echo '256 SHA256:key demo (ED25519)'", "stat": ""}
			if step != "absent" {
				writeProbeFile(t, path, "private key")
				writeProbeFile(t, path+".pub", "public key\n")
				commands[step] = failedLookup
			}
			runner := shellStepRunner(t, commands)
			read, err := configured(t, runner).Read(p.ReadRequest{ID: path, Urn: urnOf("SshKey", "demo")})
			if step != "absent" {
				requirePrivateCommandError(t, err, step, "17")
			} else if err != nil || read.ID != "" {
				t.Fatalf("absent key returned ID %q, %v", read.ID, err)
			}
		})
	}
}

func TestFstabEntryReadFailureAndAbsence(t *testing.T) {
	for _, tc := range []struct{ name, content, findmnt, id string }{
		{"mount table lookup fails", "src /demo ext4 defaults 0 0\n", failedLookup, ""},
		{"target is unmounted", "src /demo ext4 defaults 0 0\n", `echo '{"filesystems":[]}'`, "/demo"},
		{"entry is absent", "src /other ext4 defaults 0 0\n", failedLookup, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fstab")
			writeProbeFile(t, path, tc.content)
			runner := shellStepRunner(t, map[string]string{"cat": "", "findmnt": tc.findmnt})
			read, err := configured(t, runner).Read(p.ReadRequest{ID: "/demo", Urn: urnOf("FstabEntry", "demo"), Properties: props(map[string]property.Value{
				"file": property.New(path), "target": property.New("/demo"),
			})})
			if tc.name == "mount table lookup fails" {
				requirePrivateCommandError(t, err, "findmnt", "17")
			} else if err != nil || read.ID != tc.id {
				t.Fatalf("fstab entry returned ID %q, %v", read.ID, err)
			}
		})
	}
}

func TestFstabEntryReadNormalizesMountTargets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "fstab")
	const target = "/mnt//demo/."
	writeProbeFile(t, file, "src "+target+" ext4 defaults 0 0\n")
	runner := shellStepRunner(t, map[string]string{
		"cat": "", "findmnt": `echo '{"filesystems":[{"target":"/mnt/demo/","source":"src","fstype":"ext4","options":"rw"}]}'`,
	})
	read, err := configured(t, runner).Read(p.ReadRequest{ID: target, Urn: urnOf("FstabEntry", "demo"), Properties: props(map[string]property.Value{
		"file": property.New(file), "target": property.New(target),
	})})
	if err != nil || read.ID != target {
		t.Fatalf("read returned ID %q, %v", read.ID, err)
	}
	mounted := read.Properties.Get("mounted")
	if !mounted.IsString() || mounted.AsString() != "src ext4 rw" {
		t.Fatalf("mounted: %v", mounted)
	}
}

func TestUserDeleteFailureAndAbsence(t *testing.T) {
	req := p.DeleteRequest{ID: "demo", Urn: urnOf("User", "demo"), Properties: props(map[string]property.Value{
		"name": property.New("demo"), "uid": property.New(1000.0), "shell": property.New("/bin/sh"),
		"home": property.New("/home/demo"), "groups": property.New([]property.Value{}),
		"createHome": property.New(false), "allowGroupRemoval": property.New(false),
	})}
	for _, tc := range []struct {
		name, lookup, remove, failed string
		wantRemove                   bool
	}{
		{"userdel fails", "echo 'demo:x:1000:1000::/home/demo:/bin/sh'", failedLookup, "userdel", true},
		{"account is absent", "exit 2", failedLookup, "", false},
		{"account disappears during delete", "echo 'demo:x:1000:1000::/home/demo:/bin/sh'", "exit 6", "", true},
		{"UID changed", "echo 'demo:x:1001:1001::/home/demo:/bin/sh'", failedLookup, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := shellStepRunner(t, map[string]string{"getent": tc.lookup, "userdel": tc.remove})
			err := configured(t, runner).Delete(req)
			if tc.failed != "" {
				requirePrivateCommandError(t, err, tc.failed, "17")
			} else if err != nil {
				t.Fatal(err)
			}
			if runner.Ran("userdel") != tc.wantRemove {
				t.Fatalf("unexpected userdel call: %v", runner.Commands())
			}
		})
	}
}

func TestSystemdInstanceDeleteFailureAndAbsence(t *testing.T) {
	for _, tc := range []struct{ name, load string }{
		{"disable fails", "loaded"}, {"unit is absent", "not-found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := shellStepRunner(t, map[string]string{"systemctl": "case $1 in show) printf 'LoadState=" + tc.load + "\\nActiveState=inactive\\n' ;; disable) " + failedLookup + " ;; *) exit 98 ;; esac"})
			err := configured(t, runner).Delete(p.DeleteRequest{ID: "demo@one.service", Urn: urnOf("SystemdInstance", "demo"), Properties: props(map[string]property.Value{
				"template": property.New("demo"), "instance": property.New("one"), "suffix": property.New("service"),
				"unit": property.New("demo@one.service"), "enabled": property.New(true), "started": property.New(true),
			})})
			if tc.load == "loaded" {
				requirePrivateCommandError(t, err, "systemctl disable --now", "17")
			} else if err != nil || runner.Ran("systemctl disable") {
				t.Fatalf("absent instance attempted disable: %v", err)
			}
		})
	}
}

func TestSystemdUnitDeleteFailureAndAbsence(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux file utilities, including mv -T")
	}
	for _, tc := range []struct{ name, failure string }{
		{"disable fails", "disable"}, {"reload fails and restores file", "daemon-reload"}, {"file is absent", ""}, {"file changed", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "demo.service")
			const unit = "[Service]\nExecStart=/bin/true\n"
			if tc.failure != "" {
				writeProbeFile(t, path, unit)
			} else if tc.name == "file changed" {
				writeProbeFile(t, path, "[Service]\nExecStart=/bin/false\n")
			}
			show := "printf 'LoadState=not-found\\nActiveState=inactive\\n'"
			if tc.name != "file is absent" {
				show = "echo FragmentPath=" + shellQuote(path)
			}
			runner := shellStepRunner(t, map[string]string{
				"systemctl": "case $1 in show) " + show + " ;; " + tcStep(tc.failure) + "*) exit 0 ;; esac",
				"realpath":  "", "sha256sum": "", "mktemp": "", "mv": "", "rmdir": "", "rm": "",
			})
			err := configured(t, runner).Delete(testSystemdUnitDeleteRequest(dir, unit))
			if tc.failure != "" {
				requirePrivateCommandError(t, err, "systemctl "+tc.failure, "17")
				if content, err := os.ReadFile(path); err != nil || string(content) != unit {
					t.Fatalf("unit after failed delete: %q, %v", content, err)
				}
			} else if err != nil || (tc.name == "file is absent" && (runner.Ran("systemctl disable") || runner.Ran("systemctl daemon-reload"))) {
				t.Fatalf("delete returned %v, commands: %v", err, runner.Commands())
			}
			if tc.name == "file changed" {
				if content, err := os.ReadFile(path); err != nil || string(content) != "[Service]\nExecStart=/bin/false\n" {
					t.Fatalf("unverified unit after delete: %q, %v", content, err)
				}
			}
		})
	}
}

func testSystemdUnitDeleteRequest(directory, unit string) p.DeleteRequest {
	state := props(map[string]property.Value{
		"name": property.New("demo"), "unit": property.New(unit), "directory": property.New(directory),
		"enabled": property.New(true), "started": property.New(true), "mode": property.New("0644"), "unitFileState": property.New("enabled"),
	})
	return p.DeleteRequest{ID: "demo", Urn: urnOf("SystemdUnit", "demo"), Properties: state}
}

func TestSystemdUnitDeleteCanonicalPathAndLegacyDigest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux file utilities, including mv -T")
	}
	const unit = "[Service]\nExecStart=/bin/true\n"
	for _, tc := range []struct {
		name, recorded, onDisk string
		alias                  bool
	}{
		{"symlink alias and trailing slashes", unit, unit, true},
		{"legacy state without final newline", strings.TrimSuffix(unit, "\n"), unit, false},
		{"on-disk file without final newline", strings.TrimSuffix(unit, "\n"), strings.TrimSuffix(unit, "\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			file := filepath.Join(dir, "demo.service")
			writeProbeFile(t, file, tc.onDisk)
			directory := dir
			if tc.alias {
				directory = filepath.Join(t.TempDir(), "lib")
				if err := os.Symlink(dir, directory); err != nil {
					t.Fatal(err)
				}
				directory += "//"
			}
			log := filepath.Join(t.TempDir(), "systemctl.log")
			runner := shellStepRunner(t, map[string]string{
				"systemctl": "case $1 in show) echo FragmentPath=" + shellQuote(file) + " ;; " +
					"disable|daemon-reload) echo \"$1\" >> " + shellQuote(log) + " ;; *) exit 98 ;; esac",
				"realpath": "", "sha256sum": "", "mktemp": "", "mv": "", "rmdir": "", "rm": "",
			})
			if err := configured(t, runner).Delete(testSystemdUnitDeleteRequest(directory, tc.recorded)); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatalf("unit still present: %v", err)
			}
			if got, err := os.ReadFile(log); err != nil || string(got) != "disable\ndaemon-reload\n" {
				t.Fatalf("systemctl calls: %q, %v", got, err)
			}
		})
	}
}

func TestSystemdUnitDeleteMissingFragmentStillStopsLoadedUnit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses GNU realpath options")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "demo.service")
	log := filepath.Join(t.TempDir(), "systemctl.log")
	runner := shellStepRunner(t, map[string]string{
		"systemctl": "case $1 in show) printf 'LoadState=loaded\\nActiveState=active\\nFragmentPath=%s\\n' " + shellQuote(file) +
			" ;; disable|daemon-reload) echo \"$1\" >> " + shellQuote(log) + " ;; *) exit 98 ;; esac",
		"realpath": "",
	})
	if err := configured(t, runner).Delete(testSystemdUnitDeleteRequest(dir, "[Service]\n")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(log); err != nil || string(got) != "disable\ndaemon-reload\n" {
		t.Fatalf("systemctl calls: %q, %v", got, err)
	}
}

func tcStep(step string) string {
	if step == "" {
		return ""
	}
	return step + ") " + failedLookup + " ;; "
}

func TestSwapDeleteFailureAndAbsence(t *testing.T) {
	for _, tc := range []struct{ name, unit string }{
		{"unmask fails", "dphys-swapfile.service masked enabled"}, {"unit is absent", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := shellStepRunner(t, map[string]string{"systemctl": "case $1 in list-unit-files) printf '%s\\n' " + shellQuote(tc.unit) + " ;; unmask) " + failedLookup + " ;; *) exit 98 ;; esac"})
			state := props(map[string]property.Value{
				"path": property.New("/demo"), "dphys": property.New("masked"), "enabled": property.New(false),
				"unit": property.New("dphys-swapfile"), "fstabFile": property.New("/etc/fstab"), "zram": property.New(false),
				"active": property.New([]property.Value{}), "fstab": property.New([]property.Value{}), "otherSwap": property.New("allow"),
			})
			err := configured(t, runner).Delete(p.DeleteRequest{ID: "/demo", Urn: urnOf("Swap", "demo"), Properties: state})
			if tc.unit != "" {
				requirePrivateCommandError(t, err, "systemctl unmask", "17")
			} else if err != nil || runner.Ran("systemctl unmask") {
				t.Fatalf("absent swap unit attempted unmask: %v", err)
			}
		})
	}
}

func TestSwapDisableFailureAndInactivePath(t *testing.T) {
	for _, tc := range []struct{ name, active, failed string }{
		{"swapoff fails", "/demo file 1024 0 -2", "swapoff"},
		{"disable fails", "", "disable"},
		{"mask fails", "", "mask"},
		{"swap and helper are absent", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fstab")
			writeProbeFile(t, path, "/demo none swap sw 0 0\n/other none swap sw 0 0\n")
			units := ""
			if tc.failed == "disable" || tc.failed == "mask" {
				units = "dphys-swapfile.service enabled enabled"
			}
			runner := shellStepRunner(t, map[string]string{
				"cat": "printf 'Filename Type Size Used Priority\\n%s\\n' " + shellQuote(tc.active),
				"awk": "", "mv": "", "swapoff": failedLookup,
				"systemctl": "case $1 in list-unit-files) printf '%s\\n' " + shellQuote(units) + " ;; " + tcStep(tc.failed) + "*) exit 0 ;; esac",
			})
			machine := &machine{runner: runner, cfg: remote.Config{Escalation: "sudo"}}
			err := disableSwap(t.Context(), machine, "dphys-swapfile", path, sp("/demo"))
			if tc.failed != "" {
				requirePrivateCommandError(t, err, tc.failed, "17")
			} else if err != nil || runner.Ran("swapoff") || runner.Ran("systemctl disable") || runner.Ran("systemctl mask") {
				t.Fatalf("inactive swap changed: %v, commands: %v", err, runner.Commands())
			}
			if tc.failed == "" {
				want := "#/demo none swap sw 0 0\n/other none swap sw 0 0\n"
				if contents, err := os.ReadFile(path); err != nil || string(contents) != want {
					t.Fatalf("fstab: %q, %v", contents, err)
				}
			}
		})
	}
}
