package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// An import names the credential keys in its id, so they come back as secrets and never as plain
// settings, and a declaration of the same remote diffs clean.
func TestRcloneRemoteImportKeepsCredentialsSecret(t *testing.T) {
	fake := remotetest.New()
	fake.When(func(c string) bool { return strings.Contains(c, "config dump") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: `{"garage":{"type":"s3","provider":"Other","endpoint":"http://127.0.0.1:3900","access_key_id":"OBSCURED-ID","secret_access_key":"OBSCURED-KEY"}}`}
	}, 0)
	fake.When(func(c string) bool { return strings.Contains(c, "reveal") && strings.Contains(c, "OBSCURED-ID") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: "GKplain\n"}
	}, 0)
	fake.When(func(c string) bool { return strings.Contains(c, "reveal") && strings.Contains(c, "OBSCURED-KEY") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: "secretplain\n"}
	}, 0)
	s := configuredServer(t, fake)
	urn := urnOf("RcloneRemote", "garage")

	read, err := s.Read(p.ReadRequest{ID: "garage@access_key_id,secret_access_key", Urn: urn})
	if err != nil || read.ID != "garage" {
		t.Fatalf("read %+v %v", read, err)
	}
	for _, m := range []property.Map{read.Properties, read.Inputs} {
		settings := m.Get("settings").AsMap()
		if settings.Get("access_key_id").IsString() || settings.Get("secret_access_key").IsString() {
			t.Fatalf("a credential landed in the plain settings: %+v", settings)
		}
		if !m.Get("secrets").HasSecrets() {
			t.Fatalf("the secrets are not marked secret: %+v", m.Get("secrets"))
		}
	}

	declared := property.NewMap(map[string]property.Value{
		"remote": property.New("garage"), "type": property.New("s3"),
		"settings": property.New(map[string]property.Value{
			"provider": property.New("Other"), "endpoint": property.New("http://127.0.0.1:3900"),
		}),
		"secrets": property.New(map[string]property.Value{
			"access_key_id": property.New("GKplain"), "secret_access_key": property.New("secretplain"),
		}).WithSecret(true),
	})
	diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: declared})
	if err != nil || diff.HasChanges {
		t.Fatalf("the declared remote diffs %+v %v", diff, err)
	}
}
