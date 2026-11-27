package rclone

import (
	"reflect"
	"strings"
	"testing"
)

const dump = `{"archive":{"type":"sftp","host":"far.example","user":"admin","pass":"obscured-Ab3xQ"},` +
	`"backup":{"type":"s3","provider":"Wasabi"}}`

func TestParseDump(t *testing.T) {
	t.Run("finds a remote and everything stored for it", func(t *testing.T) {
		want := map[string]string{"type": "sftp", "host": "far.example", "user": "admin", "pass": "obscured-Ab3xQ"}
		if got := ParseDump(dump, "archive"); !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	})
	t.Run("says nothing rather than something wrong about a remote that is not there", func(t *testing.T) {
		if got := ParseDump(dump, "nothing"); got != nil {
			t.Errorf("got %#v", got)
		}
	})
	t.Run("survives output that is not JSON at all", func(t *testing.T) {
		for _, out := range []string{"", "Usage:\n  rclone [flags]"} {
			if got := ParseDump(out, "archive"); got != nil {
				t.Errorf("ParseDump(%q) = %#v", out, got)
			}
		}
	})
	t.Run("handles a config with no remotes in it", func(t *testing.T) {
		if got := ParseDump("{}", "archive"); got != nil {
			t.Errorf("got %#v", got)
		}
	})
}

func TestParseDumpOrdered(t *testing.T) {
	settings, keys := ParseDumpOrdered(`{"demo":{"b":"first","2":"two","type":"local","a":"last","10":"ten","b":"second"}}`, "demo")
	if strings.Join(keys, ",") != "2,10,b,type,a" || settings["b"] != "second" {
		t.Fatalf("settings %v, keys %v", settings, keys)
	}
}
