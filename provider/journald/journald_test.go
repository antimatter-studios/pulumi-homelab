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
		file := JournaldFile(map[string]string{"Storage": "volatile", "SystemMaxUse": "200M"})
		if !strings.Contains(file, "[Journal]\nStorage=volatile\nSystemMaxUse=200M\n") {
			t.Errorf("got %q", file)
		}
		if !strings.HasPrefix(file, "# Managed by Pulumi.") {
			t.Errorf("got %q", file)
		}
	})
	t.Run("round-trips through the parser it will be read back with", func(t *testing.T) {
		settings := map[string]string{"Storage": "volatile", "MaxRetentionSec": "7day"}
		want := map[string]string{"Storage": "volatile", "MaxRetentionSec": "7day"}
		if got := ParseEffective(JournaldFile(settings)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})
}

// The settings arrive at the TypeScript provider through protobuf with their keys sorted, so the
// files on machines are in key order whatever order the program declared; the Go writes the same.
func TestJournaldFileWritesSettingsInKeyOrder(t *testing.T) {
	file := JournaldFile(map[string]string{"SystemMaxUse": "200M", "Storage": "volatile", "MaxRetentionSec": "7day"})
	want := "[Journal]\nMaxRetentionSec=7day\nStorage=volatile\nSystemMaxUse=200M\n"
	if !strings.HasSuffix(file, want) {
		t.Errorf("got %q, want it to end %q", file, want)
	}
}

// JavaScript's trim keeps U+0085 (NEL), where Go's unicode.IsSpace would strip it.
func TestJsTrimKeepsNEL(t *testing.T) {
	if got := jsTrim(" a\u0085"); got != "a\u0085" {
		t.Errorf("got %q", got)
	}
}

// Review of #98: keys in the order a protobuf Struct delivered them to the TypeScript, which
// jscompat.SortedKeys reproduces: array-index keys numerically first, then UTF-16 order.
func TestJournaldFileUsesTheStructKeyOrder(t *testing.T) {
	got := JournaldFile(map[string]string{"b": "1", "10": "x", "2": "y", "Ａ": "w", "\U0001F600": "v", "A": "z"})
	want := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n[Journal]\n2=y\n10=x\nA=z\nb=1\n\U0001F600=v\nＡ=w\n"
	if got != want {
		t.Errorf("got\n%q\nwant\n%q", got, want)
	}
}
