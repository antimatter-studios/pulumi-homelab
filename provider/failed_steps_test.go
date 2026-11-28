package homelab

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Run the provider's real shell command with stub utilities. A canned nonzero
// reply would miss failures the shell accidentally converts to success.
func shellStepRunner(t *testing.T, utilities map[string]string) *remotetest.Runner {
	t.Helper()
	bin := t.TempDir()
	for name, body := range utilities {
		target := filepath.Join(bin, name)
		if body == "" {
			source, err := exec.LookPath(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(source, target); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(target, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return remotetest.New().When(func(string) bool { return true }, func(command string) remotetest.Reply {
		if strings.HasPrefix(command, "sudo -n sh -c ") {
			command = unescalate(command)
		}
		cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", command)
		cmd.Env = append(os.Environ(), "PATH="+bin)
		out, err := cmd.Output()
		reply := remotetest.Reply{Stdout: string(out)}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			reply.Code, reply.Stderr = exit.ExitCode(), string(exit.Stderr)
		} else {
			reply.Err = err
		}
		return reply
	}, 0)
}

func requireStepError(t *testing.T, err error, detail string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), detail) {
		t.Fatalf("wanted error containing %q, got %v", detail, err)
	}
}

func TestSambaConfigInstallFailure(t *testing.T) {
	runner := shellStepRunner(t, map[string]string{
		"mktemp": "", "cat": "", "rm": "", "testparm": "exit 0",
		"install": "echo 'install denied' >&2; exit 17", "smbcontrol": "exit 1",
	})
	m := &machine{runner: runner, cfg: remote.Config{Escalation: "sudo"}}
	requireStepError(t, applyConf(t.Context(), m, baseConf, smbConf), "(exit 17): install denied")
}

func TestSambaUserDeleteFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		list   remotetest.Reply
		detail string
		delete bool
	}{
		{"delete failure", remotetest.Reply{Stdout: "chris:1000:\n"}, "passdb is locked", true},
		{"lookup failure", remotetest.Reply{Code: 2, Stderr: "cannot read passdb"}, "cannot read passdb", false},
		{"already absent", remotetest.Reply{Stdout: "other:1001:\n"}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := remotetest.New().On(asRoot("pdbedit -L"), tc.list).
				On(asRoot("smbpasswd -x 'chris'"), remotetest.Reply{Code: 1, Stderr: "passdb is locked"})
			state := props(map[string]property.Value{
				"name": property.New("chris"), "exists": property.New(true), "created": property.New(true),
			})
			err := configured(t, fake).Delete(p.DeleteRequest{ID: "chris", Urn: sambaURN, Properties: state})
			if tc.detail != "" {
				requireStepError(t, err, tc.detail)
			} else if err != nil {
				t.Fatal(err)
			}
			if fake.Ran("smbpasswd -x") != tc.delete {
				t.Fatalf("unexpected delete attempt: %v", fake.Commands())
			}
		})
	}
}

func TestTunnelDeleteFailureAndAbsence(t *testing.T) {
	for _, present := range []bool{true, false} {
		name := "already absent"
		if present {
			name = "disable failed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			unit := testTunnel.Unit
			path := unitPath(unit, dir)
			if present {
				if err := os.WriteFile(path, []byte("unit"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fake := shellStepRunner(t, map[string]string{"rm": "", "systemctl": `
case "$1" in
  disable) echo 'stop refused' >&2; exit 22 ;;
  show) printf 'LoadState=not-found\nActiveState=inactive\n' ;;
  daemon-reload) exit 0 ;;
  *) exit 99 ;;
esac`})
			state := tunnelInputs(testTunnel.Forwards...).
				Set("directory", property.New(dir)).
				Set("unit", property.New(unit)).
				Set("file", property.New("unit")).
				Set("active", property.New(present)).
				Set("running", forwardsValue()).
				Set("restarts", property.New(0.0))
			err := configured(t, fake).Delete(p.DeleteRequest{ID: unit, Urn: tunnelURN, Properties: state})
			if present {
				requireStepError(t, err, "(exit 22): stop refused")
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("unit file was removed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSwapEnableStopsOnFailedSwapon(t *testing.T) {
	dir := t.TempDir()
	path, fstab := filepath.Join(dir, "swap"), filepath.Join(dir, "fstab")
	if err := os.WriteFile(fstab, nil, 0600); err != nil {
		t.Fatal(err)
	}
	fake := shellStepRunner(t, map[string]string{
		"stat": "echo ext4", "fallocate": "exit 0", "chmod": "exit 0", "mkswap": "exit 0",
		"swapon": "echo 'swapon denied' >&2; exit 18", "grep": "", "systemctl": "exit 0",
	})
	m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
	dphys := "enabled"
	_, err := m.must(t.Context(), m.escalate(swapEnableCommand(path, 1, false, &dphys, swapDphys, fstab)))
	requireStepError(t, err, "(exit 18): swapon denied")
	data, err := os.ReadFile(fstab)
	if err != nil || len(data) != 0 {
		t.Fatalf("fstab changed after swapon failed: %q, %v", data, err)
	}
}

func TestJournaldWriteFailureSkipsRestart(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(journaldPath("test", dir), 0700); err != nil {
		t.Fatal(err)
	}
	fake := shellStepRunner(t, map[string]string{
		"mkdir": "", "cat": "", "chmod": "", "systemctl": "echo 'unexpected restart' >&2; exit 0",
	})
	m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
	_, err := settleJournald(t.Context(), m, "test", JournaldArgs{Directory: &dir, Settings: map[string]string{"Storage": "persistent"}})
	requireStepError(t, err, "Is a directory")
	if strings.Contains(err.Error(), "unexpected restart") {
		t.Fatal(err)
	}
}

func TestSambaSettingRejectsInvalidApply(t *testing.T) {
	fake := remotetest.New()
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := property.NewMap(map[string]property.Value{
		"share": property.New("global"), "key": property.New("workgroup"), "value": property.New("LAB"),
		"apply": property.New("restart"),
	})
	check, err := s.Check(p.CheckRequest{Urn: settingURN, Inputs: inputs})
	if err != nil || len(check.Failures) != 1 || check.Failures[0].Property != "apply" {
		t.Fatalf("check %+v: %v", check, err)
	}
	_, err = s.Create(p.CreateRequest{Urn: settingURN, Properties: inputs})
	requireStepError(t, err, "invalid SambaSetting apply")
	if len(fake.Commands()) != 0 {
		t.Fatalf("invalid input contacted the remote: %v", fake.Commands())
	}
}

func TestTunnelInstallRequiresSystemdAnalyze(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name, detail := "missing", "systemd-analyze: not found"
		utilities := map[string]string{"mktemp": "", "rm": "", "cat": "", "install": "exit 0", "systemctl": "exit 0"}
		if invalid {
			name, detail = "verify failed", "invalid unit"
			utilities["systemd-analyze"] = "echo 'invalid unit' >&2; exit 1"
		}
		t.Run(name, func(t *testing.T) {
			fake := shellStepRunner(t, utilities)
			m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
			path := unitPath(testTunnel.Unit, t.TempDir())
			_, err := m.must(t.Context(), m.escalate(writeCommand(testTunnel.Unit, path, tunnelUnit(testTunnel))))
			requireStepError(t, err, detail)
		})
	}
}

func TestProtectedFileReadStopsOnDigestFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "protected")
	if err := os.WriteFile(path, []byte("material"), 0600); err != nil {
		t.Fatal(err)
	}
	statCalled := filepath.Join(t.TempDir(), "stat-called")
	fake := shellStepRunner(t, map[string]string{
		"sha256sum": "echo 'digest denied' >&2; exit 9", "cut": "",
		"stat": "printf called > " + shellQuote(statCalled) + "; echo '600 root root'",
	})
	_, err := configured(t, fake).Read(p.ReadRequest{ID: path, Urn: urnOf("ProtectedFile", "protected")})
	requireStepError(t, err, "digest denied")
	if _, err := os.Stat(statCalled); !os.IsNotExist(err) {
		t.Fatalf("stat ran after the failed digest: %v", err)
	}
}

func TestArchiveDeleteReportsPrefixRemovalFailure(t *testing.T) {
	dir := t.TempDir()
	prefix, link := filepath.Join(dir, "archive"), filepath.Join(dir, "current")
	if err := os.Mkdir(prefix, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(prefix, link); err != nil {
		t.Fatal(err)
	}
	fake := shellStepRunner(t, map[string]string{
		"bash": "", "find": "", "sort": "", "sha256sum": "", "dirname": "", "mktemp": "",
		"mv": "", "rmdir": "", "readlink": "",
		"rm": "echo 'prefix removal denied' >&2; exit 10",
	})
	m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
	tree, err := m.must(t.Context(), m.escalate(archiveTreeFunctions+"\ntree_identity "+shellQuote(prefix)))
	if err != nil {
		t.Fatal(err)
	}
	err = guardedDelete(t.Context(), m, prefix, archiveDeleteCommand(prefix, link, strings.TrimSpace(tree)))
	requireStepError(t, err, "prefix removal denied")
	if _, err := os.Stat(prefix); err != nil {
		t.Fatalf("staged prefix was not restored: %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != prefix {
		t.Fatalf("link after failed prefix removal: %q, %v", target, err)
	}
}

func TestAuthorizedKeyWriteFailureSkipsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_keys")
	called := filepath.Join(t.TempDir(), "attributes-called")
	attribute := "printf called > " + shellQuote(called)
	fake := shellStepRunner(t, map[string]string{
		"mkdir": "", "cat": "echo 'key write denied' >&2; exit 17",
		"chmod": attribute, "chown": attribute,
	})
	m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
	_, err := m.must(t.Context(), m.escalate(authorizedWriteCommand(path, "chris", "ssh-ed25519 AAAA test")))
	requireStepError(t, err, "(exit 17): key write denied")
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("file attributes changed after failed write: %v", err)
	}
}

func TestSystemdUnitWriteFailureSkipsSystemctl(t *testing.T) {
	dir := t.TempDir()
	called := filepath.Join(dir, "unit-step-called")
	step := "printf called > " + shellQuote(called)
	fake := shellStepRunner(t, map[string]string{
		"cat": "echo 'unit write denied' >&2; exit 17", "chmod": step, "systemctl": step,
	})
	m := &machine{runner: fake, cfg: remote.Config{Escalation: "sudo"}}
	wanted := unitWanted(SystemdUnitArgs{Name: "example", Directory: &dir, Unit: "[Service]\nExecStart=/bin/true"})
	_, err := settleUnit(t.Context(), m, wanted)
	requireStepError(t, err, "(exit 17): unit write denied")
	if _, err := os.Stat(called); !os.IsNotExist(err) {
		t.Fatalf("a later unit step ran after failed write: %v", err)
	}
}
