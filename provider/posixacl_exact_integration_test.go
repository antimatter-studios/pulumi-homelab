//go:build integration

package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// A secret file that inherited a share's readable default ACL, closed by an exact PosixAcl with its
// owner, group and mode entries, against real getfacl, setfacl and chown. The content is never
// read: the test checks it is byte for byte what it was.
func TestPosixAclExactRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	d.sh(t, "groupadd rwgroup && groupadd rogroup && useradd -G rogroup smbguest && useradd t3code && useradd jonmc && usermod -aG rwgroup chris && "+
		"mkdir -p /srv/docs && chmod 0755 /srv/docs && setfacl -m default:group:rogroup:r-x,default:other::r-x /srv/docs && "+
		"printf 'SECRET=x\\n' > /srv/docs/.env.vault && chown chris:rwgroup /srv/docs/.env.vault")
	digest := d.sh(t, "sha256sum /srv/docs/.env.vault")
	readable := func(user string) bool {
		return d.sh(t, "su -s /bin/sh "+user+" -c 'test -r /srv/docs/.env.vault' && echo yes || echo no") == "yes\n"
	}
	if !readable("smbguest") || !readable("jonmc") {
		t.Fatal("the setup did not leave the file open")
	}

	inputs := property.NewMap(map[string]property.Value{
		"path":  property.New("/srv/docs/.env.vault"),
		"owner": property.New("root"), "group": property.New("rwgroup"), "exact": property.New(true),
		"entries": entriesValue([3]string{"user", "", "rw-"}, [3]string{"group", "", "rw-"}, [3]string{"other", "", "---"},
			[3]string{"user", "t3code", "r--"}, [3]string{"user", "jonmc", "---"}),
	})
	created, err := s.Create(p.CreateRequest{Urn: aclURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(d.sh(t, "stat -c '%a %U %G' /srv/docs/.env.vault")); got != "660 root rwgroup" {
		t.Fatalf("stat says %q", got)
	}
	acl := d.sh(t, "getfacl -pc /srv/docs/.env.vault")
	for _, want := range []string{"user::rw-", "user:t3code:r--", "user:jonmc:---", "group::rw-", "mask::rw-", "other::---"} {
		if !strings.Contains(acl, want) {
			t.Errorf("getfacl lacks %s:\n%s", want, acl)
		}
	}
	if strings.Contains(acl, "rogroup") {
		t.Fatalf("the inherited reader is still there:\n%s", acl)
	}
	if readable("smbguest") || readable("jonmc") || !readable("t3code") || !readable("chris") {
		t.Fatalf("access is wrong:\n%s", acl)
	}

	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: aclURN, Properties: created.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: read.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after read %+v %v", diff, err)
	}

	// an import of the closed file by id is no change for the same declaration
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: aclURN})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: imported.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after import %+v %v", diff, err)
	}

	// somebody reopening it by hand is drift, and the update closes it again
	d.sh(t, "setfacl -m group:rogroup:r,other::r /srv/docs/.env.vault")
	drifted, err := s.Read(p.ReadRequest{ID: created.ID, Urn: aclURN, Properties: read.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: aclURN, State: drifted.Properties, Inputs: inputs})
	if err != nil || diff.DetailedDiff["entries"].Kind != p.Update {
		t.Fatalf("drift %+v %v", diff, err)
	}
	updated, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: aclURN, State: drifted.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if readable("smbguest") || strings.Contains(d.sh(t, "getfacl -pc /srv/docs/.env.vault"), "rogroup") {
		t.Fatal("the update did not close it again")
	}

	// delete removes the named entries it declared and leaves the mode and owner
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: aclURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	acl = d.sh(t, "getfacl -pc /srv/docs/.env.vault")
	if strings.Contains(acl, "t3code") || strings.Contains(acl, "jonmc") || !strings.Contains(acl, "other::---") {
		t.Fatalf("after delete:\n%s", acl)
	}
	if got := strings.TrimSpace(d.sh(t, "stat -c '%U %G' /srv/docs/.env.vault")); got != "root rwgroup" {
		t.Fatalf("after delete stat says %q", got)
	}
	if d.sh(t, "sha256sum /srv/docs/.env.vault") != digest {
		t.Fatal("the content changed")
	}
}
