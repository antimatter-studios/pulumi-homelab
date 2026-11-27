package audit

import (
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

func TestSplitSections(t *testing.T) {
	reply := strings.Join([]string{
		"#pulumi-homelab#packages",
		"nodejs",
		"vim",
		"#pulumi-homelab#times",
		"1737480000 /var/lib/dpkg/info/vim.list",
		"#pulumi-homelab#units",
		"/etc/systemd/system/aiworld.service",
		"#pulumi-homelab#passwd",
		"root:x:0:0:root:/root:/bin/bash",
		"#pulumi-homelab#logindefs",
		"UID_MIN\t1000",
	}, "\n")
	t.Run("puts every line under the marker above it", func(t *testing.T) {
		s := SplitSections(reply)
		eq(t, s.Packages, []string{"nodejs", "vim"})
		eq(t, s.Units, []string{"/etc/systemd/system/aiworld.service"})
		eq(t, s.LoginDefs, []string{"UID_MIN\t1000"})
	})
	t.Run("gives every section an empty list rather than nothing, on a machine that answered none of it", func(t *testing.T) {
		s := SplitSections("#pulumi-homelab#packages\n#pulumi-homelab#units\n")
		eq(t, s.Passwd, []string{})
		eq(t, s.Times, []string{})
	})
}

func TestParseInstallTimes(t *testing.T) {
	t.Run("drops the architecture, because apt-mark prints the bare name", func(t *testing.T) {
		times := ParseInstallTimes([]string{"1737480000 /var/lib/dpkg/info/nodejs:arm64.list"})
		eq(t, times["nodejs"], 1737480000.0)
	})
	t.Run("keeps the later of two architectures of the same package", func(t *testing.T) {
		times := ParseInstallTimes([]string{
			"1700000000 /var/lib/dpkg/info/libc6:armhf.list",
			"1737480000 /var/lib/dpkg/info/libc6:arm64.list",
		})
		eq(t, times["libc6"], 1737480000.0)
	})
	t.Run("ignores a line it cannot read rather than recording a package installed at NaN", func(t *testing.T) {
		eq(t, len(ParseInstallTimes([]string{"no such thing", ""})), 0)
	})
}

func TestParseUnitNames(t *testing.T) {
	t.Run("reports them the way SystemdUnit takes them, without the suffix", func(t *testing.T) {
		eq(t, ParseUnitNames([]string{"/etc/systemd/system/aiworld.service"}), []string{"aiworld"})
	})
	t.Run("ignores anything that is not a service", func(t *testing.T) {
		eq(t, ParseUnitNames([]string{"/etc/systemd/system/backup.timer"}), []string{})
	})
	t.Run("does not report a service called `*` on a machine with no units of its own", func(t *testing.T) {
		eq(t, ParseUnitNames([]string{"/etc/systemd/system/*.service"}), []string{})
	})
}

func TestParseLoginUsers(t *testing.T) {
	passwd := []string{
		"root:x:0:0:root:/root:/bin/bash",
		"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin",
		"aiworld:x:997:997::/opt/app:/usr/sbin/nologin",
		"admin:x:1000:1000::/home/admin:/bin/bash",
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin",
	}
	names := func(users []UserFinding) []string {
		out := []string{}
		for _, u := range users {
			out = append(out, u.Name)
		}
		return out
	}
	t.Run("keeps the people and leaves the distribution alone", func(t *testing.T) {
		eq(t, names(ParseLoginUsers(passwd, 1000)), []string{"admin"})
	})
	t.Run("excludes nobody by uid, so it is not reported on every machine for ever", func(t *testing.T) {
		for _, name := range names(ParseLoginUsers(passwd, 500)) {
			if name == "nobody" {
				t.Errorf("nobody reported")
			}
		}
	})
	t.Run("finds a system service account when the machine’s own policy puts the line lower", func(t *testing.T) {
		eq(t, names(ParseLoginUsers(passwd, 900)), []string{"aiworld", "admin"})
	})
	t.Run("skips a line that is not a passwd entry", func(t *testing.T) {
		eq(t, ParseLoginUsers([]string{"getent: command not found"}, 1000), []UserFinding{})
	})
}

func TestParseUIDMin(t *testing.T) {
	t.Run("reads the machine’s own answer", func(t *testing.T) {
		eq(t, ParseUIDMin([]string{"UID_MIN\t1000"}), 1000.0)
		eq(t, ParseUIDMin([]string{"UID_MIN 500"}), 500.0)
	})
	t.Run("falls back to the conventional answer when the machine does not say", func(t *testing.T) {
		eq(t, ParseUIDMin([]string{}), 1000.0)
	})
}
