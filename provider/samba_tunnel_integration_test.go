//go:build integration

package homelab

import (
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

func realMachineConfig() remote.Config {
	return remote.Config{Address: "127.0.0.1", Port: 20022, User: "chris", Escalation: "sudo"}
}

// The container has no samba and no systemd, so the commands those resources call are stood in
// for by scripts; everything else in the resources' shell (the heredoc staging, testparm gating,
// install, the section and line editing through cat) runs for real in Debian 13's dash.
func shims(t *testing.T, d dockerRunner) {
	t.Helper()
	d.sh(t, `mkdir -p /etc/samba && printf '[global]\n   workgroup = WORKGROUP\n\n[homes]\n   browseable = no\n' > /etc/samba/smb.conf
cat > /usr/local/bin/testparm <<'X'
#!/bin/sh
# testparm -s FILE parses; testparm -sv FILE prints it
for last; do :; done
grep -q BROKEN "$last" && exit 1
[ "$1" = -sv ] && cat "$last"
exit 0
X
printf '#!/bin/sh\necho "$@" >> /tmp/systemctl.log\n' > /usr/local/bin/systemctl
printf '#!/bin/sh\nexit 0\n' > /usr/local/bin/smbcontrol
printf '#!/bin/sh\n[ "$1" = verify ] && test -s "$2"\n' > /usr/local/bin/systemd-analyze
chmod 755 /usr/local/bin/testparm /usr/local/bin/systemctl /usr/local/bin/smbcontrol /usr/local/bin/systemd-analyze`)
}

func TestSambaRealShell(t *testing.T) {
	d := startDebianShell(t)
	shims(t, d)
	s := realServer(t, d)
	share := property.NewMap(map[string]property.Value{
		"share": property.New("photos"), "path": property.New("/mnt/photos"), "config": property.New(smbConf),
		"settings": property.New(property.NewMap(map[string]property.Value{"read only": property.New("no")})),
	})
	created, err := s.Create(p.CreateRequest{Urn: shareURN, Properties: share})
	if err != nil {
		t.Fatal(err)
	}
	setting := property.NewMap(map[string]property.Value{
		"share": property.New("global"), "key": property.New("server string"), "value": property.New("homelab's"),
		"config": property.New(smbConf),
	})
	set, err := s.Create(p.CreateRequest{Urn: settingURN, Properties: setting})
	if err != nil || set.Properties.Get("effective").AsString() != "homelab's" {
		t.Fatalf("setting %+v %v", set, err)
	}
	conf := d.sh(t, "cat /etc/samba/smb.conf; stat -c '%a %U' /etc/samba/smb.conf")
	for _, want := range []string{"[photos]\n   path = /mnt/photos\n   read only = no", "server string = homelab's", "[homes]\n   browseable = no", "644 root"} {
		if !strings.Contains(conf, want) {
			t.Errorf("smb.conf lacks %q:\n%s", want, conf)
		}
	}

	// a file the result would not parse is never installed
	// (inside [homes], which the update leaves alone; anything after [photos] belongs to its section)
	d.sh(t, "sed -i 's/browseable = no/browseable = no\\n   # BROKEN/' /etc/samba/smb.conf")
	before := d.sh(t, "cat /etc/samba/smb.conf")
	if _, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: shareURN, State: created.Properties, Inputs: share.Set("path", property.New("/mnt/other"))}); err == nil ||
		!strings.Contains(err.Error(), "the resulting smb.conf does not parse") {
		t.Fatalf("broken: %v", err)
	}
	if d.sh(t, "cat /etc/samba/smb.conf") != before {
		t.Fatal("a file that does not parse was installed")
	}
	d.sh(t, "sed -i '/BROKEN/d' /etc/samba/smb.conf")

	// each delete removes only its own part of the file
	if err := s.Delete(p.DeleteRequest{ID: set.ID, Urn: settingURN, Properties: set.Properties}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: shareURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	conf = d.sh(t, "cat /etc/samba/smb.conf")
	if strings.Contains(conf, "photos") || strings.Contains(conf, "server string") || !strings.Contains(conf, "workgroup = WORKGROUP") || !strings.Contains(conf, "[homes]\n   browseable = no") {
		t.Fatalf("after deletes:\n%s", conf)
	}
}

// The unit is staged and installed. Delete disables an edited unit and keeps its file.
func TestSshTunnelUnitFileRealShell(t *testing.T) {
	d := startDebianShell(t)
	shims(t, d)
	path := unitPath(testTunnel.Unit, testTunnel.Directory)
	file := tunnelUnit(testTunnel)
	m := &machine{runner: d, cfg: realMachineConfig()}
	if _, err := m.must(t.Context(), m.escalate(writeCommand(testTunnel.Unit, path, file))); err != nil {
		t.Fatal(err)
	}
	if got := d.sh(t, "cat "+path); got != file {
		t.Fatalf("installed %q", got)
	}
	d.sh(t, "echo '# edited' >> "+path)
	digest := digestOf([]byte(file))
	if err := deleteTunnel(t.Context(), m, testTunnel.Unit, path, &digest); err != nil {
		t.Fatalf("delete of edited unit: %v", err)
	}
	d.sh(t, "grep -Fx '# edited' "+path)
	if err := deleteTunnel(t.Context(), m, testTunnel.Unit, path, nil); err != nil {
		t.Fatalf("delete with legacy state: %v", err)
	}
	d.sh(t, "grep -Fx '# edited' "+path)
	d.sh(t, "sed -i '/^# edited$/d' "+path)
	if err := deleteTunnel(t.Context(), m, testTunnel.Unit, path, &digest); err != nil {
		t.Fatal(err)
	}
	d.sh(t, "test ! -e "+path)
	if log := d.sh(t, "cat /tmp/systemctl.log"); !strings.Contains(log, "disable --now "+testTunnel.Unit+".service") || !strings.Contains(log, "daemon-reload") {
		t.Fatalf("systemctl saw %q", log)
	}
}

func TestSambaUserPasswordRealShell(t *testing.T) {
	const password = " leading and trailing it's a canary "
	for _, escalation := range []string{"none", "sudo", "sudo-password"} {
		t.Run(escalation, func(t *testing.T) {
			d := startDebianShell(t)
			d.sh(t, `cat > /usr/local/bin/smbpasswd <<'SHIM'
#!/bin/sh
printf '%s\n' "$*" > /tmp/smbpasswd-argv
IFS= read -r first
IFS= read -r second
[ "$first" = "$second" ] || exit 2
printf '%s' "$first" > /tmp/smbpasswd-input
if [ "$3" = failing ]; then
    printf '%s' "$first" >&2
    exit 1
fi
SHIM
chmod 755 /usr/local/bin/smbpasswd`)
			cfg := realMachineConfig()
			cfg.Escalation, cfg.SudoPassword = escalation, "unused-under-NOPASSWD"
			m := &machine{runner: d, cfg: cfg}
			if err := setSambaPassword(t.Context(), m, "chris", password); err != nil {
				t.Fatal(err)
			}
			got := d.sh(t, "cat /tmp/smbpasswd-input")
			if got != password {
				t.Fatalf("smbpasswd did not receive the password verbatim")
			}
			if argv := d.sh(t, "cat /tmp/smbpasswd-argv"); strings.TrimSpace(argv) != "-s -a chris" {
				t.Fatalf("unexpected smbpasswd argv")
			}
			err := setSambaPassword(t.Context(), m, "failing", password)
			if err == nil || strings.Contains(err.Error(), strings.TrimSpace(password)) || !strings.Contains(err.Error(), "smbpasswd failed for user failing") {
				t.Fatal("a smbpasswd failure must name the user and carry none of smbpasswd's output")
			}
		})
	}
}
