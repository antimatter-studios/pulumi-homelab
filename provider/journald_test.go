package homelab

import (
	"reflect"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/journald"
)

const catConfig = `# /etc/systemd/journald.conf
#  SPDX-License-Identifier: LGPL-2.1-or-later
[Journal]
#Storage=auto
#Compress=yes
#SystemMaxUse=

# /etc/systemd/journald.conf.d/logs.conf
[Journal]
Storage=volatile
SystemMaxUse=200M

# /etc/systemd/journald.conf.d/zz-override.conf
[Journal]
Storage=persistent
`

// Ported from src/resources/journald.test.ts: noticing that something else won.
func TestOverriddenBy(t *testing.T) {
	effective := journald.ParseEffective(catConfig)
	if got := overriddenBy(map[string]string{"Storage": "volatile", "SystemMaxUse": "200M"}, effective); !reflect.DeepEqual(got, []string{"Storage"}) {
		t.Errorf("names the setting another file overrides: %v", got)
	}
	if got := overriddenBy(map[string]string{"SystemMaxUse": "200M"}, effective); len(got) != 0 {
		t.Errorf("says nothing when the whole drop-in is in force: %v", got)
	}
	if got := overriddenBy(map[string]string{"Storrage": "volatile"}, effective); !reflect.DeepEqual(got, []string{"Storrage"}) {
		t.Errorf("reports a setting journald never understood: %v", got)
	}
}

func TestParseJournaldRead(t *testing.T) {
	content := journald.JournaldFile(map[string]string{"Storage": "volatile", "SystemMaxUse": "200M"})
	out := content + "#pulumi-homelab#effective\n" + catConfig + "#pulumi-homelab#persistent\ndirectory\n"
	got := parseJournaldRead(out, "90-logs", map[string]string{"Storage": "volatile"}, journaldDirectory, journaldPersistent)
	if got.Content != content || got.Effective["Storage"] != "persistent" || !reflect.DeepEqual(got.Overridden, []string{"Storage"}) || !got.Persistent || got.JournalDir != "directory" {
		t.Errorf("%+v", got)
	}
	if absent := parseJournaldRead(content+"#pulumi-homelab#effective\n#pulumi-homelab#persistent\nabsent\n", "f", nil, "d", "j"); absent.Persistent {
		t.Errorf("%+v", absent)
	}
}
