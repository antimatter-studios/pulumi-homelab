package homelab

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func fwd(remote, local int, more ...string) TunnelForward {
	f := TunnelForward{Remote: remote, Local: local}
	if len(more) > 0 && more[0] != "" {
		f.Bind = &more[0]
	}
	if len(more) > 1 {
		f.What = &more[1]
	}
	return f
}

var testTunnel = resolvedTunnel{
	To: "tunnel@bastion.example.com", Port: 10022, Identity: "/home/chris/.ssh/id_ed25519", RunAs: "chris",
	Forwards:   []TunnelForward{fwd(20022, 22, "", "ssh"), fwd(20080, 80, "", "ingress"), fwd(20443, 443, "", "ingress, tls")},
	RestartSec: 10, Unit: "autossh-bastion-example-com-10022", Directory: "/etc/systemd/system",
}

func minimalTunnel() SshTunnelArgs {
	return SshTunnelArgs{To: "a@b", Identity: "/k", RunAs: "u", Forwards: []TunnelForward{fwd(1, 2)}}
}

// Ported from src/resources/tunnel.test.ts › spelling a reverse forward.
func TestForwardFlag(t *testing.T) {
	t.Run("publishes a far port and reaches a near one", func(t *testing.T) {
		if got := forwardFlag(fwd(20022, 22)); got != "20022:localhost:22" {
			t.Fatal(got)
		}
	})
	t.Run("passes a bind address through for the far end to accept or ignore", func(t *testing.T) {
		if got := forwardFlag(fwd(20080, 80, "0.0.0.0")); got != "0.0.0.0:20080:localhost:80" {
			t.Fatal(got)
		}
	})
	t.Run("uses localhost rather than an address, which the far end resolves here", func(t *testing.T) {
		if !strings.Contains(forwardFlag(fwd(1, 2)), "localhost") {
			t.Fatal(forwardFlag(fwd(1, 2)))
		}
	})
}

// Ported from src/resources/tunnel.test.ts › refusing a configuration that bans you.
func TestRestartRefusal(t *testing.T) {
	t.Run("accepts ten seconds and anything above the floor", func(t *testing.T) {
		if restartRefusal(10) != "" || restartRefusal(tunnelRestartFloor) != "" {
			t.Fatal("refused")
		}
	})
	t.Run("refuses a tight restart loop, and says why rather than just no", func(t *testing.T) {
		r := restartRefusal(1)
		if !strings.Contains(r, "fail2ban") || !strings.Contains(r, "ExitOnForwardFailure") {
			t.Fatal(r)
		}
		want := "RestartSec of 1s is too short. ExitOnForwardFailure makes ssh exit when a port is still held by the previous connection, systemd restarts it, and a tight loop is a login attempt every 1 seconds until fail2ban bans this address — which takes out every tunnel and the route needed to fix them. Use 10s, or at least 5s."
		if r != want {
			t.Fatalf("got %q", r)
		}
	})
	t.Run("refuses two forwards wanting the same far port", func(t *testing.T) {
		r := forwardsRefusal([]TunnelForward{fwd(20022, 22), fwd(20022, 2222)})
		if !strings.Contains(r, "20022") || !strings.Contains(r, "ExitOnForwardFailure") {
			t.Fatal(r)
		}
	})
	t.Run("refuses a tunnel with no forwards", func(t *testing.T) {
		if !strings.Contains(forwardsRefusal(nil), "not a tunnel") {
			t.Fatal(forwardsRefusal(nil))
		}
	})
	t.Run("refuses something that is not a port", func(t *testing.T) {
		if forwardsRefusal([]TunnelForward{fwd(0, 22)}) == "" || forwardsRefusal([]TunnelForward{fwd(22, 70000)}) == "" {
			t.Fatal("accepted")
		}
	})
	t.Run("allows the same near port published twice on different far ports", func(t *testing.T) {
		if r := forwardsRefusal([]TunnelForward{fwd(20022, 22), fwd(30022, 22)}); r != "" {
			t.Fatal(r)
		}
	})
}

// Ported from src/resources/tunnel.test.ts › composing the command.
func TestExecStart(t *testing.T) {
	command := execStart(testTunnel)
	t.Run("carries every forward on one connection", func(t *testing.T) {
		if strings.Count(command, "-R ") != 3 || strings.Count(command, "autossh") != 1 {
			t.Fatal(command)
		}
	})
	t.Run("exits rather than forwarding nothing", func(t *testing.T) {
		if !strings.Contains(command, "ExitOnForwardFailure=yes") {
			t.Fatal(command)
		}
	})
	t.Run("turns autossh monitoring off and keeps the connection alive from inside it", func(t *testing.T) {
		if !strings.Contains(command, "-M 0") || !strings.Contains(command, "ServerAliveInterval=30") {
			t.Fatal(command)
		}
	})
	t.Run("never waits for a prompt nobody will see", func(t *testing.T) {
		if !strings.Contains(command, "BatchMode=yes") {
			t.Fatal(command)
		}
	})
	t.Run("offers only the declared key", func(t *testing.T) {
		if !strings.Contains(command, "IdentitiesOnly=yes") || !strings.Contains(command, "-i /home/chris/.ssh/id_ed25519") {
			t.Fatal(command)
		}
	})
	t.Run("asks for no shell, no command and no tty", func(t *testing.T) {
		if !strings.Contains(command, "-N") {
			t.Fatal(command)
		}
	})
	t.Run("is the same string every time, so a refresh does not look like a change", func(t *testing.T) {
		if execStart(testTunnel) != command {
			t.Fatal("differs")
		}
	})
	t.Run("is exactly the command the TypeScript composed", func(t *testing.T) {
		want := "/usr/bin/autossh -M 0 -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o BatchMode=yes -o IdentitiesOnly=yes -i /home/chris/.ssh/id_ed25519 -p 10022 -R 20022:localhost:22 -R 20080:localhost:80 -R 20443:localhost:443 tunnel@bastion.example.com"
		if command != want {
			t.Fatal(command)
		}
	})
}

// Ported from src/resources/tunnel.test.ts › the unit.
func TestTunnelUnit(t *testing.T) {
	unit := tunnelUnit(testTunnel)
	t.Run("survives a boot that beats its own network", func(t *testing.T) {
		for _, want := range []string{"AUTOSSH_GATETIME=0", "Wants=network-online.target", "After=network-online.target"} {
			if !strings.Contains(unit, want) {
				t.Error(want)
			}
		}
	})
	t.Run("runs as the account whose known_hosts trusts the far end", func(t *testing.T) {
		if !strings.Contains(unit, "User=chris") {
			t.Fatal(unit)
		}
	})
	t.Run("restarts always, and not immediately", func(t *testing.T) {
		if !strings.Contains(unit, "Restart=always") || !strings.Contains(unit, "RestartSec=10") {
			t.Fatal(unit)
		}
	})
	t.Run("records what each forward is for, since somebody reads this in a year", func(t *testing.T) {
		if !strings.Contains(unit, "# 20022 -> 22 (ssh)") || !strings.Contains(unit, "# 20443 -> 443 (ingress, tls)") {
			t.Fatal(unit)
		}
	})
	t.Run("is the same file every time", func(t *testing.T) {
		if tunnelUnit(testTunnel) != unit {
			t.Fatal("differs")
		}
	})
	t.Run("is exactly the file the TypeScript wrote", func(t *testing.T) {
		want := "[Unit]\nDescription=Reverse tunnel to tunnel@bastion.example.com\n# 20022 -> 22 (ssh)\n# 20080 -> 80 (ingress)\n# 20443 -> 443 (ingress, tls)\n" +
			"After=network-online.target\nWants=network-online.target\n\n[Service]\nUser=chris\nEnvironment=AUTOSSH_GATETIME=0\nExecStart=" + execStart(testTunnel) +
			"\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=multi-user.target\n"
		if unit != want {
			t.Fatalf("%q", unit)
		}
	})
}

// Ported from src/resources/tunnel.test.ts › naming the unit.
func TestUnitNameFor(t *testing.T) {
	t.Run("derives a name from where the tunnel goes", func(t *testing.T) {
		if got := unitNameFor("tunnel@bastion.example.com", 10022); got != "autossh-bastion-example-com-10022" {
			t.Fatal(got)
		}
	})
	t.Run("keeps the port, so two tunnels to one host are two units", func(t *testing.T) {
		if unitNameFor("a@h", 22) == unitNameFor("a@h", 2222) {
			t.Fatal("same")
		}
	})
	t.Run("leaves nothing systemd would read as a type suffix", func(t *testing.T) {
		if strings.Contains(unitNameFor("tunnel@h.example.com", 22), ".") {
			t.Fatal(unitNameFor("tunnel@h.example.com", 22))
		}
	})
	t.Run("copes with a destination that names no user", func(t *testing.T) {
		if got := unitNameFor("bastion.example.com", 22); got != "autossh-bastion-example-com-22" {
			t.Fatal(got)
		}
	})
	t.Run("puts the unit where the layout says, not where a constant says", func(t *testing.T) {
		if got := unitPath("x", "/run/systemd/transient"); got != "/run/systemd/transient/x.service" {
			t.Fatal(got)
		}
	})
	t.Run("replaces per UTF-16 code unit, as the JavaScript replace did", func(t *testing.T) {
		if got := unitNameFor("a@h😀é", 22); got != "autossh-h----22" {
			t.Fatal(got)
		}
	})
}

// Ported from src/resources/tunnel.test.ts › comparing what is declared against what is running.
func TestSameForwards(t *testing.T) {
	t.Run("agrees regardless of the order either side lists them in", func(t *testing.T) {
		if !sameForwards([]TunnelForward{fwd(20080, 80), fwd(20022, 22)}, []TunnelForward{fwd(20022, 22), fwd(20080, 80)}) {
			t.Fatal("disagrees")
		}
	})
	t.Run("ignores the comment, which is documentation rather than behaviour", func(t *testing.T) {
		if !sameForwards([]TunnelForward{fwd(1, 2)}, []TunnelForward{fwd(1, 2, "", "anything")}) {
			t.Fatal("disagrees")
		}
	})
	t.Run("reports a forward the process is not carrying", func(t *testing.T) {
		if sameForwards([]TunnelForward{fwd(20022, 22)}, testTunnel.Forwards) {
			t.Fatal("agrees")
		}
	})
	t.Run("reports a forward that reaches a different near port", func(t *testing.T) {
		if sameForwards([]TunnelForward{fwd(20022, 22)}, []TunnelForward{fwd(20022, 2222)}) {
			t.Fatal("agrees")
		}
	})
	t.Run("does not ignore a bind address, since the far end may honour it", func(t *testing.T) {
		if sameForwards([]TunnelForward{fwd(1, 2)}, []TunnelForward{fwd(1, 2, "0.0.0.0")}) {
			t.Fatal("agrees")
		}
	})
}

// Ported from src/resources/tunnel.test.ts › refusing to start a tunnel that cannot connect.
func TestPreconditionRefusal(t *testing.T) {
	t.Run("is quiet when the key is readable and the far end is trusted", func(t *testing.T) {
		if r := preconditionRefusal(true, true, testTunnel); r != "" {
			t.Fatal(r)
		}
	})
	t.Run("names the account when it cannot read the key", func(t *testing.T) {
		r := preconditionRefusal(false, true, testTunnel)
		if !strings.Contains(r, "chris") || !strings.Contains(r, "/home/chris/.ssh/id_ed25519") {
			t.Fatal(r)
		}
	})
	t.Run("gives the command that fixes an untrusted host key", func(t *testing.T) {
		r := preconditionRefusal(true, false, testTunnel)
		if !strings.Contains(r, "ssh-keyscan") || !strings.Contains(r, "bastion.example.com") || !strings.Contains(r, "-p 10022") {
			t.Fatal(r)
		}
	})
	t.Run("reports the key before the host key, since one is checked first", func(t *testing.T) {
		if r := preconditionRefusal(false, false, testTunnel); !strings.Contains(r, "cannot read") {
			t.Fatal(r)
		}
	})
}

// Ported from src/resources/tunnel.test.ts › settling the declaration.
func TestResolveTunnel(t *testing.T) {
	t.Run("defaults the port, the restart interval and the layout", func(t *testing.T) {
		got, err := resolveTunnel(minimalTunnel())
		if err != nil || got.Port != 22 || got.RestartSec != 10 || got.Directory != "/etc/systemd/system" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("derives the unit name when none is given, and keeps one that is", func(t *testing.T) {
		got, _ := resolveTunnel(minimalTunnel())
		named := minimalTunnel()
		mine := "mine"
		named.Unit = &mine
		kept, _ := resolveTunnel(named)
		if got.Unit != "autossh-b-22" || kept.Unit != "mine" {
			t.Fatal(got.Unit, kept.Unit)
		}
	})
	t.Run("raises the refusals once rather than at each use", func(t *testing.T) {
		fast := minimalTunnel()
		one := 1.0
		fast.RestartSec = &one
		if _, err := resolveTunnel(fast); err == nil || !strings.Contains(err.Error(), "fail2ban") {
			t.Fatal(err)
		}
		none := minimalTunnel()
		none.Forwards = []TunnelForward{}
		if _, err := resolveTunnel(none); err == nil || !strings.Contains(err.Error(), "not a tunnel") {
			t.Fatal(err)
		}
	})
	t.Run("is the same answer every time", func(t *testing.T) {
		a, _ := resolveTunnel(minimalTunnel())
		b, _ := resolveTunnel(minimalTunnel())
		if !reflect.DeepEqual(a, b) {
			t.Fatal("differs")
		}
	})
}

// Ported from src/resources/tunnel.test.ts › installing the unit without installing a broken one.
func TestWriteCommand(t *testing.T) {
	file := tunnelUnit(testTunnel)
	command := writeCommand("autossh-b-22", "/etc/systemd/system/autossh-b-22.service", file)
	lines := strings.Split(command, "\n")
	t.Run("never leaves the terminator sharing a line with a command", func(t *testing.T) {
		alone := false
		for _, line := range lines {
			if line == "PULUMI_EOF" {
				alone = true
			} else if strings.HasPrefix(line, "PULUMI_EOF") {
				t.Errorf("shared: %q", line)
			}
		}
		if !alone {
			t.Fatal("no terminator")
		}
	})
	t.Run("writes the unit body verbatim, with nothing of the command in it", func(t *testing.T) {
		body := strings.Join(lines[1:len(lines)-2], "\n")
		if body+"\n" != file || strings.Contains(body, "systemctl") || strings.Contains(body, "install -m") {
			t.Fatalf("%q", body)
		}
	})
	t.Run("stages, verifies, installs, then reloads, in that order", func(t *testing.T) {
		at := func(s string) int { return strings.Index(command, s) }
		if !(at("mktemp -d") < at("systemd-analyze verify") && at("systemd-analyze verify") < at("install -m 0644") && at("install -m 0644") < at("systemctl daemon-reload")) {
			t.Fatal(command)
		}
	})
	t.Run("writes to a private staging directory rather than over the live unit", func(t *testing.T) {
		if !strings.Contains(command, "mktemp -d") || !strings.Contains(command, `cat > "$dir/autossh-b-22.service"`) || strings.Count(command, "/etc/systemd/system") != 1 {
			t.Fatal(command)
		}
	})
	t.Run("cleans the staging directory up whether or not it succeeded", func(t *testing.T) {
		if !strings.Contains(command, `trap 'rm -rf "$dir"' EXIT`) {
			t.Fatal(command)
		}
	})
	t.Run("groups the verify, since && and || associate left to right", func(t *testing.T) {
		if !strings.Contains(command, "{ ! command -v systemd-analyze >/dev/null || systemd-analyze verify") || !strings.Contains(command, "; }") {
			t.Fatal(command)
		}
	})
	t.Run("requires the verify where systemd-analyze exists and skips it where it does not", func(t *testing.T) {
		if !strings.Contains(command, "! command -v systemd-analyze") || !strings.Contains(command, "systemd-analyze verify") {
			t.Fatal(command)
		}
	})
	t.Run("does not enable or restart anything, which cannot follow a terminator", func(t *testing.T) {
		if strings.Contains(command, "systemctl enable") || strings.Contains(command, "systemctl restart") {
			t.Fatal(command)
		}
	})
	t.Run("quotes the destination path", func(t *testing.T) {
		if !strings.Contains(writeCommand("u", "/etc/it's.service", "a\n"), `'/etc/it'\''s.service'`) {
			t.Fatal("unquoted")
		}
	})
	t.Run("is the same string every time", func(t *testing.T) {
		if writeCommand("u", "/p", "a\n") != writeCommand("u", "/p", "a\n") {
			t.Fatal("differs")
		}
	})
}

// Ported from src/resources/tunnel.test.ts › refusing a unit name that is not one.
func TestUnitRefusal(t *testing.T) {
	t.Run("accepts what systemd accepts", func(t *testing.T) {
		if unitRefusal("autossh-b-22") != "" || unitRefusal("tunnel@host") != "" {
			t.Fatal("refused")
		}
	})
	t.Run("refuses the suffix, which is added", func(t *testing.T) {
		if !strings.Contains(unitRefusal("x.service"), "should not carry") {
			t.Fatal(unitRefusal("x.service"))
		}
	})
	t.Run("refuses anything that would become shell syntax in a path", func(t *testing.T) {
		for _, bad := range []string{"a b", "a;rm -rf /", "a$(id)", "a'b", "a/b", "a`id`"} {
			if unitRefusal(bad) == "" {
				t.Error(bad)
			}
		}
	})
	t.Run("is raised when the declaration settles, not at each use", func(t *testing.T) {
		args := minimalTunnel()
		bad := "a b"
		args.Unit = &bad
		if _, err := resolveTunnel(args); err == nil || !regexp.MustCompile(`systemd unit name`).MatchString(err.Error()) {
			t.Fatal(err)
		}
	})
}
