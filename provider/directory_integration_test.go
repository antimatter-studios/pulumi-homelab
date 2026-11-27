//go:build integration

package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Ported from src/local.test.ts › telling a directory from a file, which ran real stat against a
// temp directory; here the provider reads paths in a Debian 13 container.
func TestReadDirectoryRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	d.sh(t, "mkdir -m 0750 /tmp/dir && chown chris:chris /tmp/dir && touch /tmp/file")

	t.Run("reads a directory and its mode", func(t *testing.T) {
		read, err := s.Read(p.ReadRequest{ID: "/tmp/dir", Urn: directoryURN})
		if err != nil {
			t.Fatal(err)
		}
		if read.Properties.Get("mode").AsString() != "0750" || read.Properties.Get("owner").AsString() != "chris" ||
			read.Properties.Get("group").AsString() != "chris" {
			t.Fatalf("read %v", read.Properties)
		}
	})
	t.Run("refuses a path that is a file rather than reporting it absent", func(t *testing.T) {
		_, err := s.Read(p.ReadRequest{ID: "/tmp/file", Urn: directoryURN})
		if err == nil || !strings.Contains(err.Error(), "/tmp/file is a regular empty file on chris@127.0.0.1:20022, not a directory") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("reports nothing for a path with nothing at it", func(t *testing.T) {
		read, err := s.Read(p.ReadRequest{ID: "/tmp/nothing", Urn: directoryURN})
		if err != nil || read.ID != "" {
			t.Fatalf("read %+v %v", read, err)
		}
	})
}

// Create makes parents and settles mode and ownership on a real machine; delete is rmdir, which
// keeps a directory with something in it and fails, and removes an empty one.
func TestDirectoryRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	inputs := property.NewMap(map[string]property.Value{
		"path": property.New("/srv/a/shared"), "mode": property.New("0775"), "owner": property.New("chris"),
		"group": property.New("root"), "setgid": property.New(true), "sticky": property.New(true),
	})
	created, err := s.Create(p.CreateRequest{Urn: directoryURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(d.sh(t, "stat -c '%a %U %G' /srv/a/shared")); got != "3775 chris root" {
		t.Fatalf("stat says %q", got)
	}
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: directoryURN, Properties: created.Properties, Inputs: inputs})
	if err != nil || read.Properties.Get("mode").AsString() != "3775" {
		t.Fatalf("read %+v %v", read, err)
	}
	diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: directoryURN, State: read.Properties, Inputs: inputs})
	if err != nil || diff.HasChanges {
		t.Fatalf("a directory read back from the machine diffs: %+v %v", diff, err)
	}

	d.sh(t, "touch /srv/a/shared/work")
	err = s.Delete(p.DeleteRequest{ID: created.ID, Urn: directoryURN, Properties: created.Properties})
	if err == nil || !strings.Contains(err.Error(), "Directory not empty") {
		t.Fatalf("deleting a non-empty directory: %v", err)
	}
	d.sh(t, "test -f /srv/a/shared/work")

	d.sh(t, "rm /srv/a/shared/work")
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: directoryURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	d.sh(t, "test ! -e /srv/a/shared && test -d /srv/a")
}
