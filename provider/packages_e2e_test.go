package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

func urnOf(typ, name string) presource.URN {
	return presource.URN("urn:pulumi:s::p::homelab:index:" + typ + "::" + name)
}

func props(m map[string]property.Value) property.Map { return property.NewMap(m) }

func configuredServer(t *testing.T, fake *remotetest.Runner) interface {
	Check(p.CheckRequest) (p.CheckResponse, error)
	Delete(p.DeleteRequest) error
	Diff(p.DiffRequest) (p.DiffResponse, error)
	Read(p.ReadRequest) (p.ReadResponse, error)
} {
	t.Helper()
	s := newServer(t, (&capture{runner: fake}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	return s
}

// A tag or branch given as the commit is refused at check, on the commit property, before
// anything runs; an unknown commit in a preview is not refused.
func TestGitCheckoutCheckRefusesARef(t *testing.T) {
	s := configuredServer(t, remotetest.New())
	resp, err := s.Check(p.CheckRequest{Urn: urnOf("GitCheckout", "w"), Inputs: props(map[string]property.Value{
		"url": property.New(gitURL), "path": property.New("/srv/w"), "commit": property.New("v1.2.3"),
	})})
	if err != nil || len(resp.Failures) != 1 || resp.Failures[0].Property != "commit" || !strings.Contains(resp.Failures[0].Reason, "git ls-remote") {
		t.Fatalf("%+v %v", resp.Failures, err)
	}
	resp, err = s.Check(p.CheckRequest{Urn: urnOf("GitCheckout", "w"), Inputs: props(map[string]property.Value{
		"url": property.New(gitURL), "path": property.New("/srv/w"), "commit": property.New(property.Computed),
	})})
	if err != nil || len(resp.Failures) != 0 {
		t.Fatalf("%+v %v", resp.Failures, err)
	}
}

// A versionPattern RE2 cannot compile is refused at check, naming the field.
func TestArchiveCheckRefusals(t *testing.T) {
	s := configuredServer(t, remotetest.New())
	base := map[string]property.Value{
		"name": property.New("w"), "url": property.New("https://x/w.tar.gz"), "sha256": property.New("a"), "prefix": property.New("/opt/w"),
	}
	resp, err := s.Check(p.CheckRequest{Urn: urnOf("Archive", "w"), Inputs: props(base)})
	if err != nil || len(resp.Failures) != 0 {
		t.Fatalf("%+v %v", resp.Failures, err)
	}
	resp, err = s.Check(p.CheckRequest{Urn: urnOf("Archive", "w"), Inputs: props(with(base, "versionPattern", property.New(`(?=v)([0-9]+)`)))})
	if err != nil || len(resp.Failures) != 1 || resp.Failures[0].Property != "versionPattern" || !strings.Contains(resp.Failures[0].Reason, "versionPattern") {
		t.Fatalf("%+v %v", resp.Failures, err)
	}
	resp, err = s.Check(p.CheckRequest{Urn: urnOf("Archive", "w"), Inputs: props(with(base, "url", property.New("https://x/download")))})
	if err != nil || len(resp.Failures) != 0 {
		t.Fatalf("%+v %v", resp.Failures, err)
	}
}

// AptPackages: a contradiction fails the diff, and delete touches nothing.
func TestAptPackagesDiffAndDelete(t *testing.T) {
	fake := remotetest.New()
	s := configuredServer(t, fake)
	state := props(map[string]property.Value{
		"present": property.New([]property.Value{property.New("ovmf")}), "absent": property.New([]property.Value{}),
		"names": property.New([]property.Value{property.New("ovmf")}), "update": property.New(false),
		"installed": property.New(map[string]property.Value{"ovmf": property.New("1")}),
		"missing":   property.New([]property.Value{}), "lingering": property.New([]property.Value{}), "auto": property.New([]property.Value{}),
	})
	same, err := s.Diff(p.DiffRequest{ID: "apt-packages", Urn: urnOf("AptPackages", "pk"), State: state,
		Inputs: props(map[string]property.Value{"present": property.New([]property.Value{property.New("ovmf")})})})
	if err != nil || same.HasChanges {
		t.Fatalf("%+v %v", same, err)
	}
	_, err = s.Diff(p.DiffRequest{ID: "apt-packages", Urn: urnOf("AptPackages", "pk"), State: state,
		Inputs: props(map[string]property.Value{"present": property.New([]property.Value{property.New("ovmf")}), "absent": property.New([]property.Value{property.New("ovmf")})})})
	if err == nil || !strings.Contains(err.Error(), "declared both present and absent") {
		t.Fatalf("got %v", err)
	}
	if err := s.Delete(p.DeleteRequest{ID: "apt-packages", Urn: urnOf("AptPackages", "pk"), Properties: state}); err != nil || len(fake.Commands()) != 0 {
		t.Fatalf("%v %v", err, fake.Commands())
	}
}

func TestReadPreservesDeclaredImportInputs(t *testing.T) {
	t.Run("GitCheckout preserves an abbreviated pin", func(t *testing.T) {
		fake := remotetest.New()
		fake.When(func(c string) bool { return strings.Contains(c, "rev-parse HEAD") }, func(string) remotetest.Reply {
			return remotetest.Reply{Stdout: gitSHA + "\n#pulumi-homelab#origin\n" + gitURL + "\n"}
		}, 0)
		s := configuredServer(t, fake)
		inputs := props(map[string]property.Value{
			"url": property.New(gitURL), "path": property.New("/srv/w"), "commit": property.New(gitSHA[:7]),
		})
		urn := urnOf("GitCheckout", "w")
		read, err := s.Read(p.ReadRequest{ID: "/srv/w", Urn: urn, Inputs: inputs})
		if err != nil || read.Inputs.Get("commit").AsString() != gitSHA[:7] || read.Properties.Get("head").AsString() != gitSHA {
			t.Fatalf("%+v %v", read, err)
		}
		diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: inputs})
		if err != nil || diff.HasChanges {
			t.Fatalf("%+v %v", diff, err)
		}
	})

	t.Run("Archive preserves a relative binary and an empty pattern", func(t *testing.T) {
		fake := remotetest.New()
		fake.When(func(c string) bool { return strings.Contains(c, "/opt/w/bin/widget") }, func(string) remotetest.Reply {
			return remotetest.Reply{Stdout: "present\n" + ownerMarker + "\nroot:root\n" + versionMarker + "\n2.0.1\n"}
		}, 0)
		s := configuredServer(t, fake)
		inputs := props(map[string]property.Value{
			"name": property.New("w"), "url": property.New("https://x/w.tar.gz"), "sha256": property.New("a"),
			"prefix": property.New("/opt/w"), "binary": property.New("bin/widget"), "versionPattern": property.New(""),
		})
		urn := urnOf("Archive", "w")
		read, err := s.Read(p.ReadRequest{ID: "w", Urn: urn, Inputs: inputs})
		if err != nil || read.ID != "w" || read.Inputs.Get("binary").AsString() != "bin/widget" ||
			read.Properties.Get("binary").AsString() != "/opt/w/bin/widget" || read.Properties.Get("version").AsString() != "" {
			t.Fatalf("%+v %v", read, err)
		}
		diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: inputs})
		if err != nil || diff.HasChanges {
			t.Fatalf("%+v %v", diff, err)
		}
	})
}
