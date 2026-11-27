package tunnel

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

func TestParseForwards(t *testing.T) {
	cmdline := strings.Join([]string{
		"/usr/bin/autossh", "-M", "0", "-N",
		"-o", "ExitOnForwardFailure=yes",
		"-i", "/home/chris/.ssh/id_ed25519",
		"-p", "10022",
		"-R", "20022:localhost:22",
		"-R", "20080:localhost:80",
		"tunnel@bastion.example.com",
	}, "\n")
	t.Run("reads every forward the process is carrying", func(t *testing.T) {
		eq(t, ParseForwards(cmdline), []Forward{{Remote: 20022, Local: 22}, {Remote: 20080, Local: 80}})
	})
	t.Run("reads a bind address when the process has one", func(t *testing.T) {
		eq(t, ParseForwards("-R\n0.0.0.0:20080:localhost:80"), []Forward{{Remote: 20080, Local: 80, Bind: "0.0.0.0"}})
	})
	t.Run("answers nothing for a process that is not there", func(t *testing.T) {
		eq(t, ParseForwards(""), []Forward{})
	})
	t.Run("ignores a -R with nothing after it", func(t *testing.T) {
		eq(t, ParseForwards("/usr/bin/autossh\n-R"), []Forward{})
	})
	t.Run("ignores anything that is not a pair of ports", func(t *testing.T) {
		eq(t, ParseForwards("-R\nnonsense"), []Forward{})
	})
	t.Run("does not mistake a local forward for a reverse one", func(t *testing.T) {
		eq(t, ParseForwards("-L\n8080:localhost:80"), []Forward{})
	})
	t.Run("trims what JavaScript trims: a NEL stays and spoils the argument", func(t *testing.T) {
		eq(t, ParseForwards("-R\u0085\n20022:localhost:22"), []Forward{})
		eq(t, ParseForwards("-R\n20022:localhost:22\u0085"), []Forward{})
		eq(t, ParseForwards("\ufeff-R\u3000\n20022:localhost:22\u00a0"), []Forward{{Remote: 20022, Local: 22}})
	})
	t.Run("reads ports with Number(): radix prefixes count, Go-only spellings do not", func(t *testing.T) {
		eq(t, ParseForwards("-R\n0x4e56:localhost:0o26"), []Forward{{Remote: 20054, Local: 22}})
		eq(t, ParseForwards("-R\n20_022:localhost:22"), []Forward{})
	})
}

func TestParseTunnelKeepsANelOnAProperty(t *testing.T) {
	eq(t, ParseTunnel("ActiveState=active\u0085\n#pulumi-homelab#cmdline\n").Active, false)
}

func TestParseTunnel(t *testing.T) {
	out := strings.Join([]string{
		"ActiveState=active",
		"NRestarts=3",
		"MainPID=1234",
		"#pulumi-homelab#cmdline",
		"-R", "20022:localhost:22",
	}, "\n")
	t.Run("reads the state, the restarts and the forwards apart", func(t *testing.T) {
		eq(t, ParseTunnel(out), Tunnel{Active: true, Restarts: 3, Running: []Forward{{Remote: 20022, Local: 22}}})
	})
	t.Run("reads properties by key rather than by position", func(t *testing.T) {
		reordered := "NRestarts=3\nMainPID=1234\nActiveState=active\n#pulumi-homelab#cmdline\n"
		eq(t, ParseTunnel(reordered).Active, true)
		eq(t, ParseTunnel(reordered).Restarts, 3)
	})
	t.Run("reads a stopped tunnel as stopped, not as absent", func(t *testing.T) {
		stopped := "ActiveState=failed\nNRestarts=97\nMainPID=0\n#pulumi-homelab#cmdline\n"
		eq(t, ParseTunnel(stopped), Tunnel{Active: false, Restarts: 97, Running: []Forward{}})
	})
	t.Run("answers zero restarts when systemd said nothing about them", func(t *testing.T) {
		eq(t, ParseTunnel("ActiveState=active\n#pulumi-homelab#cmdline\n").Restarts, 0)
	})
}
