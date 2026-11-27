package journald

import (
	"reflect"
	"sort"
	"strings"
	"testing"
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

func TestParseEffective(t *testing.T) {
	t.Run("takes the last assignment, which is the one systemd takes", func(t *testing.T) {
		if got := ParseEffective(catConfig)["Storage"]; got != "persistent" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("keeps a setting nothing else overrides", func(t *testing.T) {
		if got := ParseEffective(catConfig)["SystemMaxUse"]; got != "200M" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("does not read a commented default as a setting", func(t *testing.T) {
		if got, ok := ParseEffective(catConfig)["Compress"]; ok {
			t.Errorf("got %q, want undefined", got)
		}
	})
	t.Run("ignores section headers and the file-name banners", func(t *testing.T) {
		var keys []string
		for k := range ParseEffective(catConfig) {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if want := []string{"Storage", "SystemMaxUse"}; !reflect.DeepEqual(keys, want) {
			t.Errorf("got %v, want %v", keys, want)
		}
	})
}

func TestJournaldFile(t *testing.T) {
	t.Run("writes a [Journal] section and says who wrote it", func(t *testing.T) {
		file := JournaldFile([]Setting{{"Storage", "volatile"}, {"SystemMaxUse", "200M"}})
		if !strings.Contains(file, "[Journal]\nStorage=volatile\nSystemMaxUse=200M\n") {
			t.Errorf("got %q", file)
		}
		if !strings.HasPrefix(file, "# Managed by Pulumi.") {
			t.Errorf("got %q", file)
		}
	})
	t.Run("round-trips through the parser it will be read back with", func(t *testing.T) {
		settings := []Setting{{"Storage", "volatile"}, {"MaxRetentionSec", "7day"}}
		want := map[string]string{"Storage": "volatile", "MaxRetentionSec": "7day"}
		if got := ParseEffective(JournaldFile(settings)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}
