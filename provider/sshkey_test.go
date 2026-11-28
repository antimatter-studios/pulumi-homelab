package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Ported from src/resources/sshkey.test.ts: the files the pair lives in.

func TestKeyFiles(t *testing.T) {
	right := keyFiles{PrivateMode: "0600", PublicMode: "0644", Owner: "svc", Group: "svc"}
	if got := parseKeyFiles("600|svc|svc\n644|svc|svc\n"); got != right {
		t.Errorf("reads both halves, private first: %+v", got)
	}
	if got := parseKeyFiles("600|root|root\n644|root|root\n"); got.PrivateMode != "0600" {
		t.Errorf("mode shape: %+v", got)
	}
	if got := parseKeyFiles("600|svc|svc\n"); got.PublicMode != "" || got.PrivateMode != "0600" {
		t.Errorf("public empty when stat said nothing about it: %+v", got)
	}
	if got := parseKeyFiles(""); got != (keyFiles{}) {
		t.Errorf("empty: %+v", got)
	}
}

func TestKeyFilesPermissionsWrong(t *testing.T) {
	right := keyFiles{PrivateMode: "0600", PublicMode: "0644", Owner: "svc", Group: "svc"}
	with := func(f func(*keyFiles)) keyFiles { k := right; f(&k); return k }
	if permissionsWrong(right, "svc", "svc") {
		t.Error("content when right")
	}
	for _, k := range []keyFiles{
		with(func(k *keyFiles) { k.PrivateMode = "0644" }),
		with(func(k *keyFiles) { k.Owner = "root" }),
		with(func(k *keyFiles) { k.Group = "root" }),
		with(func(k *keyFiles) { k.PublicMode = "0600" }),
	} {
		if !permissionsWrong(k, "svc", "svc") {
			t.Errorf("%+v not reported", k)
		}
	}
	if permissionsWrong(with(func(k *keyFiles) { k.PublicMode = "" }), "svc", "svc") || permissionsWrong(keyFiles{}, "svc", "svc") {
		t.Error("says nothing about a half the machine declined to describe")
	}
}

func TestKeyFilesPermissionsCommand(t *testing.T) {
	if got := permissionsCommand("/home/svc/.ssh/id_ed25519", "svc", "svc"); !strings.Contains(got, "chmod 0600 '/home/svc/.ssh/id_ed25519'") {
		t.Error(got)
	}
	command := permissionsCommand("/k/id", "svc", "svc")
	if !strings.Contains(command, "chmod 0644 '/k/id.pub'") {
		t.Error("public half readable", command)
	}
	if strings.Index(command, "chmod 0600") > strings.Index(command, "chown") {
		t.Error("narrows before it hands over", command)
	}
	if got := permissionsCommand("/k/id", "svc", "agents"); !strings.Contains(got, "chown 'svc:agents' '/k/id' '/k/id.pub'") {
		t.Error(got)
	}
	if !strings.Contains(command, "&&") || strings.Contains(command, ";") {
		t.Error("stops on failure", command)
	}
	if got := permissionsCommand("/home/it's/id", "svc", "svc"); !strings.Contains(got, `'/home/it'\''s/id'`) {
		t.Error(got)
	}
	if command != permissionsCommand("/k/id", "svc", "svc") {
		t.Error("not stable")
	}
	if privateKeyMode != "0600" || publicKeyMode != "0644" {
		t.Error("modes")
	}
}

func TestSshKeyDeleteRefuses(t *testing.T) {
	if err := sshKeyDeleteRefusal("/k/id"); !strings.Contains(err.Error(), "refusing to delete the key at /k/id") {
		t.Error(err)
	}
}

func TestParseKeyOnDisk(t *testing.T) {
	out := "ssh-ed25519 AAAAC3x svc@host\n" + fingerprintMarker + "\n256 SHA256:abc svc@host (ED25519)\n" + statMarker + "\n600|svc|svc\n644|svc|svc\n"
	got := parseKeyOnDisk(out)
	if got.PublicKey != "ssh-ed25519 AAAAC3x svc@host" || got.Fingerprint != "SHA256:abc" || got.PrivateMode != "0600" || got.Owner != "svc" {
		t.Errorf("%+v", got)
	}
}

func TestSshKeyTypeChangeDeletesBeforeCreateAtSamePath(t *testing.T) {
	urn := urnOf("SshKey", "deploy")
	inputs := props(map[string]property.Value{
		"path": property.New("/home/chris/.ssh/deploy"), "type": property.New("ed25519"), "allowDelete": property.New(true),
	})
	s := newServer(t, nil)
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	rsa := inputs.Set("type", property.New("rsa"))
	samePath, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: created.Properties, Inputs: rsa})
	if err != nil || !samePath.DeleteBeforeReplace || samePath.DetailedDiff["type"].Kind != p.UpdateReplace {
		t.Fatalf("same-path type change: %+v, %v", samePath, err)
	}
	moved, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: urn, State: created.Properties,
		Inputs: rsa.Set("path", property.New("/home/chris/.ssh/other"))})
	if err != nil || moved.DeleteBeforeReplace || moved.DetailedDiff["path"].Kind != p.UpdateReplace {
		t.Fatalf("different-path type change: %+v, %v", moved, err)
	}
}
