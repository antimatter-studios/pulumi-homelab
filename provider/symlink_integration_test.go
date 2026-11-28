//go:build integration

package homelab

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Ported from src/local.test.ts › telling a symlink from what it points at, which ran real readlink
// against a temp directory; here the provider reads paths in a Debian 13 container.
func TestReadSymlinkRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	d.sh(t, "mkdir /tmp/real && ln -s /tmp/real /tmp/link && ln -s /mnt/never-mounted /tmp/broken")

	t.Run("reads a link’s target rather than following it", func(t *testing.T) {
		read, err := s.Read(p.ReadRequest{ID: "/tmp/link", Urn: symlinkURN})
		if err != nil || read.Properties.Get("target").AsString() != "/tmp/real" {
			t.Fatalf("read %+v %v", read, err)
		}
	})
	t.Run("refuses a real directory where a link belongs", func(t *testing.T) {
		_, err := s.Read(p.ReadRequest{ID: "/tmp/real", Urn: symlinkURN})
		if err == nil || !strings.Contains(err.Error(), "/tmp/real on chris@127.0.0.1:20022 is a directory, not a symlink. Refusing to replace it") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("reports a broken link as a link, because that is what it is", func(t *testing.T) {
		read, err := s.Read(p.ReadRequest{ID: "/tmp/broken", Urn: symlinkURN})
		if err != nil || read.Properties.Get("target").AsString() != "/mnt/never-mounted" {
			t.Fatalf("read %+v %v", read, err)
		}
	})
}

// Create makes parents and links; relinking a link to a directory replaces it rather than
// creating a link inside the directory; delete removes a link and never a real directory.
func TestSymlinkRealShell(t *testing.T) {
	d := startDebianShell(t)
	s := realServer(t, d)
	d.sh(t, "mkdir -p /mnt/one /mnt/two")
	in := func(target string) property.Map {
		return property.NewMap(map[string]property.Value{"path": property.New("/var/lib/x/journal"), "target": property.New(target)})
	}
	created, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: in("/mnt/one")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: symlinkURN, State: created.Properties, Inputs: in("/mnt/two")}); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(d.sh(t, "readlink /var/lib/x/journal; ls -A /mnt/one")); got != "/mnt/two" {
		t.Fatalf("after relinking: %q", got)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: symlinkURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	d.sh(t, "test ! -e /var/lib/x/journal && test ! -L /var/lib/x/journal")

	// something real put there since: delete leaves it alone
	d.sh(t, "mkdir /var/lib/x/journal && touch /var/lib/x/journal/system.journal")
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: symlinkURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	d.sh(t, "test -f /var/lib/x/journal/system.journal")

	// a launcher script is accepted with the flag, and left as it is
	d.sh(t, `mkdir -p /opt/bin && printf '#!/bin/sh\nexec '"'"'/opt/t3/t3'"'"' "$@"\n' > /opt/bin/t3`)
	launcher := property.NewMap(map[string]property.Value{
		"path": property.New("/opt/bin/t3"), "target": property.New("/opt/t3/old"), "acceptLauncherScript": property.New(true),
	})
	if _, err := s.Create(p.CreateRequest{Urn: symlinkURN, Properties: launcher}); err != nil {
		t.Fatal(err)
	}
	read, err := s.Read(p.ReadRequest{ID: "/opt/bin/t3", Urn: symlinkURN, Properties: launcher})
	if err != nil || read.Properties.Get("target").AsString() != "/opt/t3/t3" {
		t.Fatalf("read %+v %v", read, err)
	}
	d.sh(t, "test -f /opt/bin/t3 && test ! -L /opt/bin/t3")
}

func TestSymlinkDeleteKeepsRegularFileSwappedInBeforeRename(t *testing.T) {
	d := startDebianShell(t)
	d.sh(t, `mkdir /tmp/symlink-race && cd /tmp/symlink-race &&
ln -s /missing link && printf outside > replacement &&
cat > mv <<'SH'
#!/bin/sh
if [ "$1" = -T ]; then
    /usr/bin/mv -T -- replacement ./link || exit 1
fi
exec /usr/bin/mv "$@"
SH
chmod +x mv
chown -R chris:chris /tmp/symlink-race`)
	command := "PATH=/tmp/symlink-race:$PATH; export PATH; " + symlinkDeleteCommand("/tmp/symlink-race/link", nil)
	warnings := captureDeleteWarnings(t)
	m := &machine{runner: d, cfg: remote.Config{Escalation: "none"}}
	const path = "/tmp/symlink-race/link"
	if err := guardedDelete(t.Context(), m, path, command); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if log := warnings.String(); !strings.Contains(log, "level=WARN") || !strings.Contains(log, path) || !strings.Contains(log, "restored") {
		t.Fatalf("delete warning %q", log)
	}
	if contents := d.sh(t, "test ! -L /tmp/symlink-race/link && cat /tmp/symlink-race/link"); contents != "outside" {
		t.Fatalf("replacement contents %q", contents)
	}
}
