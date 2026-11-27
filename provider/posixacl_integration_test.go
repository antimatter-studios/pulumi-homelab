//go:build integration

package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// PosixAcl against real setfacl and getfacl: it sets its entries, leaves somebody else's alone,
// normalises directory modes, and removes only what it declared.
func TestPosixAclRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	d.sh(t, "groupadd rw && mkdir -p /mnt/pool/a/b /mnt/pool/var/c && chgrp -R rw /mnt/pool && chmod -R 2755 /mnt/pool && setfacl -m group:root:r-x /mnt/pool")
	inputs := property.NewMap(map[string]property.Value{
		"path": property.New("/mnt/pool"), "entries": entriesValue([3]string{"user", "chris", "7"}),
		"defaultEntries": entriesValue([3]string{"user", "chris", "rwx"}),
		"normalise": property.New(property.NewMap(map[string]property.Value{
			"group": property.New("rw"), "from": property.New("2755"), "to": property.New("2775"),
			"ignore": property.New([]property.Value{property.New("var")}),
		})),
	})
	created, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	acl := d.sh(t, "getfacl -pc /mnt/pool")
	for _, want := range []string{"user:chris:rwx", "default:user:chris:rwx", "group:root:r-x"} {
		if !strings.Contains(acl, want) {
			t.Errorf("getfacl lacks %s:\n%s", want, acl)
		}
	}
	// a and a/b were 2755 in group rw (the pool itself became 2775 when setfacl widened its mask);
	// var and below are pruned
	if created.Properties.Get("normalised").AsNumber() != 2 {
		t.Fatalf("normalised %v", created.Properties.Get("normalised"))
	}
	if got := strings.TrimSpace(d.sh(t, "stat -c %a /mnt/pool/a/b /mnt/pool/var/c | tr '\\n' ' '")); got != "2775 2755" {
		t.Fatalf("modes %q", got)
	}

	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: aclURN, Properties: created.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: read.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after read %+v %v", diff, err)
	}

	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: aclURN, Properties: read.Properties}); err != nil {
		t.Fatal(err)
	}
	acl = d.sh(t, "getfacl -pc /mnt/pool")
	if strings.Contains(acl, "user:chris") || !strings.Contains(acl, "group:root:r-x") {
		t.Fatalf("after delete:\n%s", acl)
	}
}
