package homelab

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

func captureDeleteWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestOldStateDeletionGuardsDiffClean(t *testing.T) {
	const file = "/srv/app/config"
	fileArgs := fileInputs(file, "original\n")
	protectedArgs := protectedInputs(fileSource("/unused"), "once")
	tunnelArgs := tunnelInputs(testTunnel.Forwards...)
	archiveArgs := props(map[string]property.Value{
		"name": property.New("app"), "prefix": property.New("/opt/app"),
		"url": property.New("https://example.com/app.tar.gz"), "sha256": property.New(strings.Repeat("a", 64)),
	})
	shareArgs := props(map[string]property.Value{"share": property.New("photos"), "path": property.New("/srv/photos")})
	userArgs := props(map[string]property.Value{"name": property.New("chris"), "password": property.New("declared").WithSecret(true)})
	tests := []struct {
		resource, id  string
		inputs, state property.Map
		guardedPath   string
	}{
		{"ManagedFile", file, fileArgs,
			fileArgs.Delete("content").Set("digest", property.New(digestOf([]byte("original\n")))), file},
		{"ProtectedFile", protectedArgs.Get("path").AsString(), protectedArgs,
			protectedArgs.Set("digest", property.New(seeded)).Set("mode", property.New("0600")),
			protectedArgs.Get("path").AsString()},
		{"SshTunnel", testTunnel.Unit, tunnelArgs,
			tunnelArgs.Set("unit", property.New(testTunnel.Unit)).Set("active", property.New(true)).
				Set("running", tunnelArgs.Get("forwards")).Set("restarts", property.New(0.0)).
				Set("file", property.New(tunnelUnit(testTunnel))), unitPath(testTunnel.Unit, testTunnel.Directory)},
		{"Archive", "app", archiveArgs,
			archiveArgs.Set("owner", property.New("root")).Set("group", property.New("root")).
				Set("installed", property.New(true)).Set("version", property.New("")).Set("versionCommand", property.New("")), ""},
		{"SambaShare", "photos", shareArgs,
			shareArgs.Set("config", property.New(smbConf)).Set("settings", property.New(map[string]property.Value{})).
				Set("actual", property.New(shareArgs.Delete("share"))).Set("parses", property.New(true)), ""},
		{"SambaUser", "chris", userArgs,
			userArgs.Delete("password").Set("exists", property.New(true)), ""},
	}
	for _, tc := range tests {
		t.Run(tc.resource, func(t *testing.T) {
			warnings := captureDeleteWarnings(t)
			fake := remotetest.New()
			if tc.guardedPath != "" {
				fake.On(asRoot(writtenFileDeleteCommand(tc.guardedPath, nil)), remotetest.Reply{Code: 10})
			}
			if tc.resource == "SshTunnel" {
				fake.When(func(c string) bool {
					return strings.HasPrefix(unescalate(c), "systemctl disable --now "+shellQuote(tc.id+".service")+" ||")
				}, func(string) remotetest.Reply { return remotetest.Reply{} }, 0)
			}
			s := configured(t, fake)
			urn := urnOf(tc.resource, tc.id)
			diff, err := s.Diff(p.DiffRequest{ID: tc.id, Urn: urn, State: tc.state, Inputs: tc.inputs})
			if err != nil || diff.HasChanges || len(diff.DetailedDiff) != 0 {
				t.Fatalf("legacy state diff: %+v, %v", diff, err)
			}
			if len(fake.Commands()) != 0 {
				t.Fatalf("legacy state diff ran remote commands: %v", fake.Commands())
			}

			if err := s.Delete(p.DeleteRequest{ID: tc.id, Urn: urn, Properties: tc.state}); err != nil {
				t.Fatalf("delete of legacy state should succeed: %v", err)
			}
			target := tc.guardedPath
			reason := "state has no digest from a provider write"
			if target == "" {
				if len(fake.Commands()) != 0 {
					t.Fatalf("legacy delete touched the machine: %v", fake.Commands())
				}
				target, reason = tc.id, "state"
			}
			log := warnings.String()
			if !strings.Contains(log, "level=WARN") || !strings.Contains(log, target) || !strings.Contains(log, reason) {
				t.Fatalf("legacy delete should warn about retaining %s, got %q", target, log)
			}
		})
	}
}

func TestAuthorizedKeyDeleteRefusesFailedRead(t *testing.T) {
	const path = "/home/chris/.ssh/authorized#keys"
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnop laptop"
	fake := remotetest.New().When(func(c string) bool {
		return strings.Contains(c, "cat --") && strings.Contains(c, path)
	}, func(string) remotetest.Reply {
		return remotetest.Reply{Stderr: "Permission denied", Code: 1}
	}, 0)
	s := configured(t, fake)
	state := props(map[string]property.Value{
		"path": property.New(path), "key": property.New(key), "user": property.New("chris"),
		"mode": property.New("0600"), "directoryMode": property.New("0700"), "owner": property.New("chris"),
		"options": property.New([]property.Value{}), "comment": property.New("laptop"),
	})
	err := s.Delete(p.DeleteRequest{ID: path + "#test", Urn: urnOf("AuthorizedKey", "laptop"), Properties: state})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") || len(fake.Commands()) != 1 {
		t.Fatalf("failed read should stop before the write: %v, commands %v", err, fake.Commands())
	}
}

func TestSambaCreatePreservesExistingEntriesOnDelete(t *testing.T) {
	t.Run("share", func(t *testing.T) {
		f := &fakeSamba{conf: baseConf + "\n[photos]\n   path = /srv/photos\n", parses: true}
		fake := f.runner()
		s := configured(t, fake)
		inputs := props(map[string]property.Value{"share": property.New("photos"), "path": property.New("/srv/photos")})
		created, err := s.Create(p.CreateRequest{Urn: shareURN, Properties: inputs})
		if err != nil {
			t.Fatal(err)
		}
		if owned := created.Properties.Get("created"); !owned.IsBool() || owned.AsBool() {
			t.Fatalf("expected an adopted share: %v", created.Properties)
		}
		before := len(fake.Commands())
		if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: shareURN, Properties: created.Properties}); err != nil ||
			len(fake.Commands()) != before || !strings.Contains(f.conf, "[photos]") {
			t.Fatalf("adopted share should be left in place: %v", err)
		}
	})
	t.Run("user", func(t *testing.T) {
		fake := remotetest.New().
			On("getent passwd 'chris'", remotetest.Reply{}).
			On(asRoot(listSambaUsers), remotetest.Reply{Stdout: "chris:1000:\n"}).
			On(asRoot("smbpasswd -s -a 'chris' >/dev/null"), remotetest.Reply{})
		s := configured(t, fake)
		inputs := props(map[string]property.Value{"name": property.New("chris"), "password": property.New("example").WithSecret(true)})
		created, err := s.Create(p.CreateRequest{Urn: sambaURN, Properties: inputs})
		if err != nil {
			t.Fatal(err)
		}
		if owned := created.Properties.Get("created"); !owned.IsBool() || owned.AsBool() {
			t.Fatalf("expected an adopted account: %v", created.Properties)
		}
		before := len(fake.Commands())
		if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: sambaURN, Properties: created.Properties}); err != nil ||
			len(fake.Commands()) != before {
			t.Fatalf("adopted Samba account should be left in place: %v", err)
		}
	})
}
