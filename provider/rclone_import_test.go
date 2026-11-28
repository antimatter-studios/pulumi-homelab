package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// An import names the obscured credential keys in its id, so they come back revealed as secrets and
// never as plain settings, and a declaration of the same remote diffs clean.
func TestRcloneRemoteImportKeepsCredentialsSecret(t *testing.T) {
	fake := remotetest.New()
	fake.When(func(c string) bool { return strings.Contains(c, "config dump") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: `{"jonflix":{"type":"webdav","url":"https://dav.example/","vendor":"other","user":"jon","pass":"is35suLTYDUARoSoPWgX_TeYnQiUhjI_Yw"}}`}
	}, 0)
	s := configuredServer(t, fake)
	urn := urnOf("RcloneRemote", "jonflix")

	read, err := s.Read(p.ReadRequest{ID: "jonflix@pass", Urn: urn})
	if err != nil || read.ID != "jonflix" {
		t.Fatalf("read %+v %v", read, err)
	}
	if fake.Ran("is35suLTYDUARoSoPWgX_TeYnQiUhjI_Yw") || fake.Ran("plainpass") {
		t.Fatal("credentials appeared in an import command")
	}
	for _, m := range []property.Map{read.Properties, read.Inputs} {
		if m.Get("settings").AsMap().Get("pass").IsString() {
			t.Fatalf("a credential landed in the settings: %+v", m.Get("settings"))
		}
		if !m.Get("secrets").IsSecret() {
			t.Fatalf("the secrets are not marked secret: %+v", m.Get("secrets"))
		}
	}

	declared := property.NewMap(map[string]property.Value{
		"remote": property.New("jonflix"), "type": property.New("webdav"),
		"settings": property.New(map[string]property.Value{
			"url": property.New("https://dav.example/"), "vendor": property.New("other"), "user": property.New("jon"),
		}),
		"secrets": property.New(map[string]property.Value{"pass": property.New("plainpass")}).WithSecret(true),
	})
	diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: declared})
	if err != nil || diff.HasChanges {
		t.Fatalf("the declared remote diffs %+v %v", diff, err)
	}
}

// An S3 remote keeps its keys in settings, in the clear. Imported by its plain name, nothing is
// revealed, the whole settings map comes back marked secret, and the declaration diffs clean.
func TestRcloneRemoteImportMarksSettingsSecret(t *testing.T) {
	fake := remotetest.New()
	fake.When(func(c string) bool { return strings.Contains(c, "config dump") }, func(string) remotetest.Reply {
		return remotetest.Reply{Stdout: `{"garage-offsite":{"type":"s3","provider":"Other","endpoint":"http://127.0.0.1:3900","access_key_id":"GKplain","secret_access_key":"secretplain"}}`}
	}, 0)
	s := configuredServer(t, fake)
	urn := urnOf("RcloneRemote", "garage-offsite")

	read, err := s.Read(p.ReadRequest{ID: "garage-offsite", Urn: urn})
	if err != nil || read.ID != "garage-offsite" {
		t.Fatalf("read %+v %v", read, err)
	}
	if fake.Ran("reveal") {
		t.Fatal("a plain setting was revealed")
	}
	for _, m := range []property.Map{read.Properties, read.Inputs} {
		if !m.Get("settings").IsSecret() {
			t.Fatalf("the settings are not marked secret: %+v", m.Get("settings"))
		}
	}

	declared := property.NewMap(map[string]property.Value{
		"remote": property.New("garage-offsite"), "type": property.New("s3"),
		"settings": property.New(map[string]property.Value{
			"provider": property.New("Other"), "endpoint": property.New("http://127.0.0.1:3900"),
			"access_key_id": property.New("GKplain"), "secret_access_key": property.New("secretplain"),
		}).WithSecret(true),
	})
	diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: declared})
	if err != nil || diff.HasChanges {
		t.Fatalf("the declared remote diffs %+v %v", diff, err)
	}

	// a refresh with state does not add the marking
	again, err := s.Read(p.ReadRequest{ID: "garage-offsite", Urn: urn, Properties: property.NewMap(map[string]property.Value{
		"remote": property.New("garage-offsite"), "type": property.New("s3"), "config": property.New("/root/.config/rclone/rclone.conf"),
		"settings": property.New(map[string]property.Value{"provider": property.New("Other")}),
		"secrets":  property.New(map[string]property.Value{}),
	})})
	if err != nil || again.Properties.Get("settings").IsSecret() {
		t.Fatalf("refresh %+v %v", again, err)
	}
}
