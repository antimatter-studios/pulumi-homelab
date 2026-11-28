package homelab

import (
	"reflect"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// Ported from src/resources/rclone.test.ts: building the command.
func TestConfigPairs(t *testing.T) {
	t.Run("writes key and value as separate arguments, as config create takes them", func(t *testing.T) {
		if got := configPairs(map[string]string{"host": "far.example", "user": "admin"}); !reflect.DeepEqual(got, []string{"host", "far.example", "user", "admin"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("sorts, so a rearranged settings map is not a different command", func(t *testing.T) {
		a := configPairs(map[string]string{"user": "admin", "host": "far.example"})
		b := configPairs(map[string]string{"host": "far.example", "user": "admin"})
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%v != %v", a, b)
		}
	})
	t.Run("keeps an empty value rather than dropping the key", func(t *testing.T) {
		if got := configPairs(map[string]string{"pass": ""}); !reflect.DeepEqual(got, []string{"pass", ""}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("has nothing to say about an empty map", func(t *testing.T) {
		if got := configPairs(map[string]string{}); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

// The create command as the TypeScript writes it, including the double space an empty map leaves.
func TestRcloneCreateCommand(t *testing.T) {
	got := rcloneCreateCommand("/srv/rclone.conf", "archive", "sftp", map[string]string{"user": "admin", "host": "h"}, map[string]string{"pass": "p"})
	want := "mkdir -p '/srv' && rclone --config '/srv/rclone.conf' config create 'archive' 'sftp' 'host' 'h' 'user' 'admin' 'pass' 'p' --obscure --non-interactive >/dev/null"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	got = rcloneCreateCommand("rclone.conf", "a", "s3", nil, nil)
	want = "mkdir -p 'rclone.conf' && rclone --config 'rclone.conf' config create 'a' 's3'  --obscure --non-interactive >/dev/null"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRcloneRemoteReadDistinguishesDumpFailureFromAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		dump remotetest.Reply
		want string
	}{
		{"command failure", remotetest.Reply{Code: 1, Stderr: "config read denied"}, "config read denied"},
		{"malformed dump", remotetest.Reply{Stdout: "unexpected output"}, "invalid rclone config dump"},
		{"malformed remote", remotetest.Reply{Stdout: `{"backup": null}`}, "invalid remote in rclone config dump"},
		{"confirmed absence", remotetest.Reply{Stdout: `{"other": {"type":"sftp"}}`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := remotetest.New().On(asRoot("rclone --config '"+rcloneDefaultConfig+"' config dump"), tc.dump)
			read, err := configuredServer(t, fake).Read(p.ReadRequest{ID: "backup", Urn: urnOf("RcloneRemote", "backup")})
			if tc.want != "" {
				requireStepError(t, err, tc.want)
			} else if err != nil || read.ID != "" {
				t.Fatalf("missing remote returned %+v, %v", read, err)
			}
		})
	}
}

func TestRcloneRemoteDeleteReportsFailureAndSkipsAbsentRemote(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
		delete bool
	}{
		{"delete failed", "printf '%s' '{\"backup\":{\"type\":\"sftp\"}}'", "config write denied", true},
		{"dump failed", "echo 'config read denied' >&2; exit 1", "config read denied", false},
		{"already absent", "printf '{}'", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := shellStepRunner(t, map[string]string{"rclone": `
test "$1" = --config && test "$2" = /srv/rclone.conf || exit 99
case "$3 $4" in
  'config dump') ` + tc.script + ` ;;
  'config delete') echo 'config write denied' >&2; exit 1 ;;
  *) exit 99 ;;
esac`})
			state := property.NewMap(map[string]property.Value{
				"config": property.New("/srv/rclone.conf"), "remote": property.New("backup"), "type": property.New("sftp"),
				"settings": property.New(property.NewMap(nil)), "secrets": property.New(property.NewMap(nil)),
			})
			err := configuredServer(t, fake).Delete(p.DeleteRequest{ID: "backup", Urn: urnOf("RcloneRemote", "backup"), Properties: state})
			if tc.want != "" {
				requireStepError(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
			if fake.Ran("config delete") != tc.delete {
				t.Fatalf("unexpected delete attempt: %v", fake.Commands())
			}
		})
	}
}
