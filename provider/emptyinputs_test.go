package homelab

import (
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// The provider library writes every empty list or map of the inputs back into the outputs, even
// for an input the state does not have; a state that cannot hold it can never be read, diffed,
// updated or deleted again. Every #45 type with a collection input is checked, created (preview)
// and diffed with each collection empty.
func TestEmptyCollectionInputsDecodeAsState(t *testing.T) {
	s := newServer(t, (&capture{runner: remotetest.New()}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	empty, none := property.New([]property.Value{}), property.New(map[string]property.Value{})
	cases := map[string]map[string]property.Value{
		"User":          {"name": property.New("svc"), "shell": property.New("/bin/sh"), "groups": empty},
		"SudoRule":      {"user": property.New("a"), "passwordless": property.New(true), "commands": empty},
		"AuthorizedKey": {"user": property.New("a"), "key": property.New("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabcdefghijklmnop x"), "options": empty},
		"KernelCmdline": {"flags": empty},
		"FstabEntry":    {"source": property.New("UUID=a"), "target": property.New("/mnt"), "type": property.New("ext4"), "options": empty},
		"Journald":      {"settings": none},
		"BootConfig":    {"settings": none, "unchecked": none, "overlays": empty},
		"SshdConfig": {"unchecked": none, "match": none, "settings": property.New(map[string]property.Value{
			"allowUsers": empty, "allowGroups": empty, "denyUsers": empty, "denyGroups": empty, "listenAddress": empty,
			"ciphers": empty, "macs": empty, "kexAlgorithms": empty})},
	}
	for typ, inputs := range cases {
		urn := urnOf(typ, "x")
		checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: property.NewMap(inputs)})
		if err != nil || len(checked.Failures) > 0 {
			t.Errorf("%s check: %+v %v", typ, checked.Failures, err)
			continue
		}
		created, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
		if err != nil {
			t.Errorf("%s create: %v", typ, err)
			continue
		}
		if _, err := s.Diff(p.DiffRequest{ID: "x", Urn: urn, State: created.Properties, Inputs: checked.Inputs}); err != nil {
			t.Errorf("%s: the state does not decode: %v", typ, err)
		}
	}
}
