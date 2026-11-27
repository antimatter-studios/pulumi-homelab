package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

func props(m map[string]property.Value) property.Map { return property.NewMap(m) }

func configuredServer(t *testing.T, fake *remotetest.Runner) interface {
	Check(p.CheckRequest) (p.CheckResponse, error)
	Delete(p.DeleteRequest) error
	Diff(p.DiffRequest) (p.DiffResponse, error)
	Read(p.ReadRequest) (p.ReadResponse, error)
	Update(p.UpdateRequest) (p.UpdateResponse, error)
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

func TestPackageImportsFromIDOnly(t *testing.T) {
	tests := []struct {
		typeName, id, importID, probe, stdout string
		declaration                           map[string]property.Value
		unknown                               []string
		retainedKey, editKey                  string
	}{
		{
			typeName: "Archive", id: "mise", importID: "mise@/opt/mise/1.0@/opt/mise/current",
			probe: "test -d '/opt/mise/current'", stdout: "present\n" + ownerMarker + "\nroot:root\n" + versionMarker + "\n",
			declaration: map[string]property.Value{
				"name": property.New("mise"), "url": property.New("https://x/mise.tar.xz"),
				"sha256": property.New(strings.Repeat("a", 64)), "prefix": property.New("/opt/mise/1.0"),
				"link": property.New("/opt/mise/current"), "binary": property.New("bin/mise"),
				"healthCommand": property.New("{}/bin/mise --version"), "strip": property.New(1.0),
			},
			unknown:     []string{"url", "sha256", "binary", "healthCommand", "strip", "format"},
			retainedKey: "prefix", editKey: "sha256",
		},
		{
			typeName: "Archive", id: "gh", importID: "gh@/opt/gh",
			probe: "test -d '/opt/gh'", stdout: "present\n" + ownerMarker + "\nt3code:t3code\n" + versionMarker + "\n",
			declaration: map[string]property.Value{
				"name": property.New("gh"), "url": property.New("https://x/gh.tar.gz"),
				"sha256": property.New(strings.Repeat("a", 64)), "prefix": property.New("/opt/gh"),
				"binary": property.New("bin/gh"), "owner": property.New("t3code"), "group": property.New("t3code"),
			},
			unknown:     []string{"url", "sha256", "link", "binary", "healthCommand", "strip", "format"},
			retainedKey: "prefix", editKey: "sha256",
		},
		{
			typeName: "OfficialInstall", id: "install-claude",
			importID: "install-claude@t3code@/mnt/storage/t3code/.local/bin/claude",
			probe:    "runuser -u 't3code' -- bash -c", stdout: "2.1.3\n",
			declaration: map[string]property.Value{
				"account": property.New("t3code"), "home": property.New("/mnt/storage/t3code"),
				"bin":       property.New("/mnt/storage/t3code/.local/bin/claude"),
				"installer": property.New("curl -fsSL https://claude.ai/install.sh | bash"),
			},
			unknown:     []string{"home", "installer"},
			retainedKey: "bin", editKey: "installer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			fake := remotetest.New()
			quotedProbe := shellQuote(tc.probe)
			probe := quotedProbe[1 : len(quotedProbe)-1]
			fake.When(func(c string) bool { return strings.Contains(c, probe) }, func(string) remotetest.Reply {
				return remotetest.Reply{Stdout: tc.stdout}
			}, 0)
			s := configuredServer(t, fake)
			urn := urnOf(tc.typeName, tc.id)
			read, err := s.Read(p.ReadRequest{ID: tc.importID, Urn: urn})
			if err != nil || read.ID != tc.id || !read.Inputs.Get(tc.retainedKey).Equals(tc.declaration[tc.retainedKey]) {
				t.Fatalf("read: %+v, %v", read, err)
			}
			for _, key := range tc.unknown {
				if !read.Properties.Get(key).IsNull() || !read.Inputs.Get(key).IsNull() {
					t.Errorf("read materialized unknown %s", key)
				}
			}
			checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: props(tc.declaration), State: read.Inputs})
			if err != nil || len(checked.Failures) != 0 {
				t.Fatalf("check: %+v, %v", checked, err)
			}
			diffReq := p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: checked.Inputs}
			diff, err := s.Diff(diffReq)
			if err != nil || !diff.HasChanges || len(diff.DetailedDiff) != 0 {
				t.Fatalf("expected only a state update: %+v, %v", diff, err)
			}
			update, err := s.Update(p.UpdateRequest{
				ID: read.ID, Urn: urn, State: read.Properties, OldInputs: read.Inputs, Inputs: checked.Inputs,
			})
			if err != nil || !update.Properties.Get(tc.editKey).Equals(tc.declaration[tc.editKey]) {
				t.Fatalf("update: %+v, %v", update, err)
			}
			if cmds := fake.Commands(); len(cmds) != 1 || !strings.Contains(cmds[0], probe) {
				t.Fatalf("expected only the import probe, got %v", cmds)
			}
			diffReq.State = update.Properties
			diff, err = s.Diff(diffReq)
			if err != nil || diff.HasChanges {
				t.Fatalf("unchanged declaration: %+v, %v", diff, err)
			}
			diffReq.Inputs = checked.Inputs.Set(tc.editKey, property.New("changed"))
			diff, err = s.Diff(diffReq)
			if err != nil || !diff.HasChanges || diff.DetailedDiff[tc.editKey].Kind != p.Update {
				t.Fatalf("changed declaration: %+v, %v", diff, err)
			}
		})
	}
}
