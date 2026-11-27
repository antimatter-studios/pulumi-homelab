package homelab

import (
	"strings"
	"testing"
)

// Ported from src/resources/sudo.test.ts.

func TestSudoersFileName(t *testing.T) {
	if got, err := sudoersFileName("admin-nopasswd"); err != nil || got != "admin-nopasswd" {
		t.Errorf("accepts an ordinary name: %q %v", got, err)
	}
	for _, name := range []string{"admin.conf", "admin~"} {
		if _, err := sudoersFileName(name); err == nil || !strings.Contains(err.Error(), "would never apply") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../sudoers", ""} {
		if _, err := sudoersFileName(name); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
}

func TestSudoersFile(t *testing.T) {
	cases := []struct {
		args SudoRuleArgs
		want string
	}{
		{SudoRuleArgs{User: strp("admin"), Passwordless: true}, "admin ALL=(ALL) NOPASSWD: ALL"},
		{SudoRuleArgs{User: strp("admin"), Passwordless: false}, "admin ALL=(ALL) ALL"},
		{SudoRuleArgs{Group: strp("sudo"), Passwordless: false}, "%sudo ALL=(ALL) ALL"},
		{SudoRuleArgs{User: strp("backup"), Passwordless: true, Commands: []string{"/usr/bin/systemctl restart borg", "/usr/bin/borg"}, RunAs: strp("root")},
			"backup ALL=(root) NOPASSWD: /usr/bin/systemctl restart borg, /usr/bin/borg"},
	}
	for _, c := range cases {
		if got, err := sudoersLine(c.args); err != nil || got != c.want {
			t.Errorf("got %q %v, want %q", got, err, c.want)
		}
	}
	for _, args := range []SudoRuleArgs{{User: strp("admin"), Group: strp("sudo"), Passwordless: true}, {Passwordless: true}} {
		if _, err := sudoersLine(args); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("%+v: %v", args, err)
		}
	}
	file, _ := sudoersFile(SudoRuleArgs{User: strp("admin"), Passwordless: true})
	if !strings.HasPrefix(file, "# Managed by Pulumi.") || !strings.HasSuffix(file, "\n") {
		t.Errorf("file %q", file)
	}
}

func TestSudoApplyCommandValidatesBeforeInstalling(t *testing.T) {
	got := sudoApplyCommand("admin", "x\n", "/etc/sudoers.d")
	want := "umask 077 && staging=$(mktemp) && cat > \"$staging\" <<'PULUMI_EOF'\nx\nPULUMI_EOF\n\n" +
		`visudo -cqf "$staging" || { rm -f "$staging"; echo "sudoers content did not parse" >&2; exit 1; }; ` +
		`install -m 0440 -o root -g root "$staging" '/etc/sudoers.d/admin' && rm -f "$staging"`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
