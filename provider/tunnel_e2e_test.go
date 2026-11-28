package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var tunnelURN = presource.URN("urn:pulumi:s::p::homelab:index:SshTunnel::bastion")

func forwardsValue(fs ...TunnelForward) property.Value {
	out := []property.Value{}
	for _, f := range fs {
		m := map[string]property.Value{"remote": property.New(float64(f.Remote)), "local": property.New(float64(f.Local))}
		if f.What != nil {
			m["what"] = property.New(*f.What)
		}
		out = append(out, property.New(property.NewMap(m)))
	}
	return property.New(out)
}

func tunnelInputs(fs ...TunnelForward) property.Map {
	return property.NewMap(map[string]property.Value{
		"to": property.New("tunnel@bastion.example.com"), "port": property.New(10022.0), "identity": property.New("/home/chris/.ssh/id_ed25519"),
		"runAs": property.New("chris"), "forwards": forwardsValue(fs...), "restartSec": property.New(10.0),
		"directory": property.New("/etc/systemd/system"),
	})
}

// fakeTunnelMachine answers the tunnel's commands with a unit file and a running process.
type fakeTunnelMachine struct {
	file      string
	installed bool
	active    bool
	running   []string
	identity  bool
	known     bool
	restarts  int
	unitRM    bool
}

func (f *fakeTunnelMachine) runner(unit string, wanted resolvedTunnel) *remotetest.Runner {
	path := unitPath(unit, "/etc/systemd/system")
	file := tunnelUnit(wanted)
	r := remotetest.New()
	r.When(func(c string) bool { return c == asRoot(probeCommand(wanted)) }, func(string) remotetest.Reply {
		out := ""
		if f.identity {
			out += "identity\n"
		}
		if f.known {
			out += "known\n"
		}
		return remotetest.Reply{Stdout: out}
	}, 0)
	r.When(func(c string) bool { return c == asRoot(readTunnelCommand(unit, "/etc/systemd/system")) }, func(string) remotetest.Reply {
		if !f.installed {
			return remotetest.Reply{Code: 9}
		}
		state := "inactive"
		if f.active {
			state = "active"
		}
		cmdline := ""
		if f.active {
			cmdline = "/usr/bin/ssh\n" + strings.Join(f.running, "\n") + "\n"
		}
		return remotetest.Reply{Stdout: f.file + tunnelFileMarker + "\nActiveState=" + state + "\nNRestarts=" + string(rune('0'+f.restarts)) + "\nMainPID=42\n" + tunnelCmdMarker + "\n" + cmdline}
	}, 0)
	r.When(func(c string) bool { return c == asRoot(writeCommand(unit, path, file)) }, func(string) remotetest.Reply {
		f.file, f.installed = file, true
		return remotetest.Reply{}
	}, 0)
	svc := shellQuote(unit + ".service")
	r.When(func(c string) bool { return c == asRoot("systemctl enable "+svc+" && systemctl restart "+svc) }, func(string) remotetest.Reply {
		f.active = true
		f.running = nil
		for _, fw := range wanted.Forwards {
			f.running = append(f.running, "-R", forwardFlag(fw))
		}
		return remotetest.Reply{}
	}, 0)
	r.When(func(c string) bool { return c == asRoot(tunnelDisableCommand(unit, path)) }, func(string) remotetest.Reply {
		f.active = false
		return remotetest.Reply{}
	}, 0)
	digest := digestOf([]byte(file))
	r.When(func(c string) bool { return c == asRoot(writtenFileDeleteCommand(path, &digest)) }, func(string) remotetest.Reply {
		if !f.installed {
			return remotetest.Reply{Code: 9}
		}
		if f.file != file {
			return remotetest.Reply{Code: 11}
		}
		f.unitRM, f.installed = true, false
		return remotetest.Reply{}
	}, 0)
	r.On(asRoot("systemctl daemon-reload"), remotetest.Reply{})
	return r
}

func TestSshTunnelLifecycle(t *testing.T) {
	wanted := testTunnel
	f := &fakeTunnelMachine{identity: true, known: true}
	fake := f.runner(wanted.Unit, wanted)
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	inputs := tunnelInputs(testTunnel.Forwards...)

	// check refuses a restart loop and bad forwards at preview
	bad, err := s.Check(p.CheckRequest{Urn: tunnelURN, Inputs: property.NewMap(map[string]property.Value{
		"to": property.New("a@b"), "identity": property.New("/k"), "runAs": property.New("u"), "forwards": forwardsValue(), "restartSec": property.New(1.0),
	})})
	if err != nil || len(bad.Failures) != 1 || bad.Failures[0].Property != "forwards" || !strings.Contains(bad.Failures[0].Reason, "fail2ban") {
		t.Fatalf("check %+v %v", bad, err)
	}

	created, err := s.Create(p.CreateRequest{Urn: tunnelURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != wanted.Unit || !created.Properties.Get("active").AsBool() || created.Properties.Get("running").AsArray().Len() != 3 {
		t.Fatalf("created %v", created.Properties)
	}

	// settled: create again runs no write or restart
	before := len(fake.Commands())
	if _, err := s.Create(p.CreateRequest{Urn: tunnelURN, Properties: inputs}); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.Commands()[before:] {
		if strings.Contains(c, "systemctl enable") || strings.Contains(c, "PULUMI_EOF") {
			t.Fatalf("a settled tunnel ran %s", c)
		}
	}

	// import by id: no diff
	imported, err := s.Read(p.ReadRequest{ID: wanted.Unit, Urn: tunnelURN})
	if err != nil || imported.Inputs.Get("to").AsString() != wanted.To {
		t.Fatalf("import %+v %v", imported, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: wanted.Unit, Urn: tunnelURN, State: imported.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("import diff %+v %v", diff, err)
	}

	// the process lost a forward: drift; a new destination replaces, deleting first
	f.running = f.running[:2]
	read, err := s.Read(p.ReadRequest{ID: wanted.Unit, Urn: tunnelURN, Properties: created.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: wanted.Unit, Urn: tunnelURN, State: read.Properties, Inputs: inputs}); err != nil || diff.DetailedDiff["forwards"].Kind != p.Update {
		t.Fatalf("drift diff %+v %v", diff, err)
	}
	moved := tunnelInputs(testTunnel.Forwards...)
	moved = moved.Set("to", property.New("tunnel@other.example.com"))
	if diff, err := s.Diff(p.DiffRequest{ID: wanted.Unit, Urn: tunnelURN, State: created.Properties, Inputs: moved}); err != nil || !diff.DeleteBeforeReplace || diff.DetailedDiff["to"].Kind != p.UpdateReplace {
		t.Fatalf("to diff %+v %v", diff, err)
	}

	// delete removes the unit it wrote
	if err := s.Delete(p.DeleteRequest{ID: wanted.Unit, Urn: tunnelURN, Properties: created.Properties}); err != nil || !f.unitRM {
		t.Fatalf("delete %v", err)
	}
	if gone, err := s.Read(p.ReadRequest{ID: wanted.Unit, Urn: tunnelURN, Properties: created.Properties}); err != nil || gone.ID != "" {
		t.Fatalf("read after delete %+v %v", gone, err)
	}
}

func TestSshTunnelDeleteAfterUnitEdit(t *testing.T) {
	wanted := testTunnel
	f := &fakeTunnelMachine{identity: true, known: true}
	s := configured(t, f.runner(wanted.Unit, wanted))
	created, err := s.Create(p.CreateRequest{Urn: tunnelURN, Properties: tunnelInputs(testTunnel.Forwards...)})
	if err != nil {
		t.Fatal(err)
	}
	f.file = "edited unit\n"
	read, err := s.Read(p.ReadRequest{ID: wanted.Unit, Urn: tunnelURN, Properties: created.Properties, Inputs: tunnelInputs(testTunnel.Forwards...)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.DeleteRequest{ID: wanted.Unit, Urn: tunnelURN, Properties: read.Properties}); err != nil ||
		f.unitRM || f.active || f.file != "edited unit\n" {
		t.Fatalf("edited unit must be disabled and kept: %v", err)
	}
}

// What stops a tunnel before anything is installed: the account cannot read the key, or does not
// trust the far end; and what fails after: the process carrying other forwards.
func TestSshTunnelRefusals(t *testing.T) {
	wanted := testTunnel
	for _, c := range []struct {
		identity, known bool
		want            string
	}{
		{false, true, "chris cannot read /home/chris/.ssh/id_ed25519 on this machine"},
		{true, false, "sudo -u chris ssh-keyscan -p 10022 bastion.example.com >> ~chris/.ssh/known_hosts"},
	} {
		f := &fakeTunnelMachine{identity: c.identity, known: c.known}
		fake := f.runner(wanted.Unit, wanted)
		_, err := configured(t, fake).Create(p.CreateRequest{Urn: tunnelURN, Properties: tunnelInputs(testTunnel.Forwards...)})
		if err == nil || !strings.Contains(err.Error(), c.want) || f.installed {
			t.Fatalf("got %v, installed %v", err, f.installed)
		}
	}

	f := &fakeTunnelMachine{identity: true, known: true, restarts: 3}
	fake := remotetest.New()
	svc := shellQuote(wanted.Unit + ".service")
	fake.When(func(c string) bool { return c == asRoot("systemctl enable "+svc+" && systemctl restart "+svc) }, func(string) remotetest.Reply {
		f.active, f.running = true, []string{"-R", "20022:localhost:22"}
		return remotetest.Reply{}
	}, 0)
	inner := f.runner(wanted.Unit, wanted)
	fake.When(func(string) bool { return true }, func(c string) remotetest.Reply {
		out, errb, code, _ := inner.Run(t.Context(), c, nil)
		return remotetest.Reply{Stdout: string(out), Stderr: string(errb), Code: code}
	}, 0)
	_, err := configured(t, fake).Create(p.CreateRequest{Urn: tunnelURN, Properties: tunnelInputs(testTunnel.Forwards...)})
	want := "started autossh-bastion-example-com-10022 on chris@127.0.0.1:20022 but the running process carries 20022:localhost:22 rather than 20022:localhost:22 20080:localhost:80 20443:localhost:443. It has restarted 3 times"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
}

func TestSshTunnelDeleteReportsDisableFailureWithoutUnitFile(t *testing.T) {
	dir := t.TempDir()
	unit := testTunnel.Unit
	fake := shellStepRunner(t, map[string]string{"systemctl": `
case "$1" in
  show) printf 'LoadState=loaded\nActiveState=active\n' ;;
  disable) echo 'loaded unit could not be stopped' >&2; exit 22 ;;
  *) exit 99 ;;
esac`})
	state := tunnelInputs(testTunnel.Forwards...).
		Set("directory", property.New(dir)).
		Set("unit", property.New(unit)).
		Set("file", property.New("unit")).
		Set("active", property.New(true)).
		Set("running", forwardsValue()).
		Set("restarts", property.New(0.0))
	err := configured(t, fake).Delete(p.DeleteRequest{ID: unit, Urn: tunnelURN, Properties: state})
	requireStepError(t, err, "(exit 22): loaded unit could not be stopped")
	if len(fake.Commands()) != 1 {
		t.Fatalf("delete continued after disable failed: %v", fake.Commands())
	}
}
