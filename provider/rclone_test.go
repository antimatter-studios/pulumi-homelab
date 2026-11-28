package homelab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/rclone"
	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

// Check the create options and parameter semantics exercised by the CLI tests.
func TestRcloneCreatePayload(t *testing.T) {
	t.Run("writes each key and value to rclone's parameters", func(t *testing.T) {
		payload, err := rcloneCreatePayload("archive", "sftp", map[string]string{"user": "admin", "host": "h"}, map[string]string{"pass": "p", "user": "override"})
		var got rcloneCreateRequest
		if err != nil || json.Unmarshal(payload, &got) != nil {
			t.Fatal("could not decode create request")
		}
		if got.Name != "archive" || got.Type != "sftp" || !got.Opt.Obscure || !got.Opt.NonInteractive {
			t.Fatal("create request has incorrect options")
		}
		want := map[string]string{"user": "override", "host": "h", "pass": "p"}
		if !reflect.DeepEqual(got.Parameters, want) {
			t.Fatal("parameters did not round-trip")
		}
	})
	t.Run("a rearranged settings map produces the same request", func(t *testing.T) {
		a, errA := rcloneCreatePayload("archive", "sftp", map[string]string{"user": "admin", "host": "h"}, nil)
		b, errB := rcloneCreatePayload("archive", "sftp", map[string]string{"host": "h", "user": "admin"}, nil)
		if errA != nil || errB != nil || string(a) != string(b) {
			t.Fatal("request depends on settings order")
		}
	})
	t.Run("keeps an empty value rather than dropping the key", func(t *testing.T) {
		payload, err := rcloneCreatePayload("archive", "sftp", nil, map[string]string{"pass": ""})
		if err != nil || !strings.Contains(string(payload), `"parameters":{"pass":""}`) {
			t.Fatal("request dropped an empty value")
		}
	})
	t.Run("has nothing to say about an empty map", func(t *testing.T) {
		payload, err := rcloneCreatePayload("archive", "sftp", nil, nil)
		if err != nil || !strings.Contains(string(payload), `"parameters":{}`) {
			t.Fatal("empty parameters were not encoded as an object")
		}
	})
}

func TestRcloneCreateCommand(t *testing.T) {
	got := rcloneCreateCommand("/srv/rclone.conf", "archive", "sftp", true)
	if !strings.HasPrefix(got, "mkdir -p '/srv' && {\n") ||
		!strings.Contains(got, "rclone --config '/srv/rclone.conf' rcd") ||
		!strings.Contains(got, "--data-binary @-") ||
		!strings.Contains(got, "/config/create") {
		t.Fatal("command did not send stdin to rclone's config API")
	}
	for _, required := range []string{
		`command -v openssl >/dev/null 2>&1 || exit 90`,
		`command -v curl >/dev/null 2>&1 || exit 91`,
		`command -v rclone >/dev/null 2>&1 || exit 92`,
		`umask 077`,
		`trap cleanup EXIT`,
		`--rc-addr 127.0.0.1:0`,
		`--rc-client-ca "$work/cert"`,
		`--cert "$work/cert" --key "$work/key"`,
		`--cacert "$work/cert"`,
		`--connect-timeout 5 --max-time 60`,
	} {
		if !strings.Contains(got, required) {
			t.Errorf("create command is missing %q", required)
		}
	}
	got = rcloneCreateCommand("rclone.conf", "a", "s3", false)
	want := "command -v rclone >/dev/null 2>&1 || exit 92\nmkdir -p 'rclone.conf' && rclone --config 'rclone.conf' config create 'a' 's3'  --obscure --non-interactive >/dev/null"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestRcloneCreateReportsMissingTools(t *testing.T) {
	for _, tc := range []struct {
		tool         string
		code         int
		noParameters bool
	}{
		{"openssl", 90, false},
		{"curl", 91, false},
		{"rclone", 92, false},
		{"rclone", 92, true},
	} {
		name := tc.tool
		if tc.noParameters {
			name += "/no parameters"
		}
		t.Run(name, func(t *testing.T) {
			fake := remotetest.New()
			fake.When(func(c string) bool {
				return strings.Contains(c, "config create") || strings.Contains(c, "/config/create")
			}, func(string) remotetest.Reply {
				return remotetest.Reply{Code: tc.code}
			}, 1)
			m := &machine{runner: fake, cfg: remote.Config{Escalation: "none", User: "user", Address: "host"}}
			args := RcloneRemoteArgs{Remote: "archive", Type: "sftp", Secrets: map[string]string{"pass": "fixture"}}
			if tc.noParameters {
				args.Secrets = nil
			}
			err := createRemote(context.Background(), m, rcloneDefaultConfig, args)
			want := tc.tool + " is not installed on user@host"
			if err == nil || err.Error() != want {
				t.Fatalf("got %v, want %q", err, want)
			}
		})
	}
}

func TestRcloneRemoteSecretTransport(t *testing.T) {
	const plain = "plainpass"
	const obscured = "is35suLTYDUARoSoPWgX_TeYnQiUhjI_Yw"
	const setting = "credential-in-settings"
	const dump = `{"archive":{"type":"sftp","host":"credential-in-settings","pass":"is35suLTYDUARoSoPWgX_TeYnQiUhjI_Yw"}}`
	const echo = plain + " " + obscured + " " + setting

	for _, escalation := range []string{"none", "sudo", "sudo-password"} {
		for _, outcome := range []string{"success", "create exit", "create connection failure", "dump connection failure"} {
			t.Run(escalation+"/"+outcome, func(t *testing.T) {
				ctx := context.Background()
				fake := remotetest.New()
				matchDump := func(c string) bool { return strings.Contains(c, "config dump") }
				fake.When(matchDump, func(string) remotetest.Reply { return remotetest.Reply{Stdout: "{}"} }, 1)
				fake.When(matchDump, func(string) remotetest.Reply {
					if outcome == "dump connection failure" {
						return remotetest.Reply{Err: errors.New(echo)}
					}
					return remotetest.Reply{Stdout: dump}
				}, 0)
				fake.When(func(c string) bool { return strings.Contains(c, "/config/create") }, func(string) remotetest.Reply {
					switch outcome {
					case "create exit":
						return remotetest.Reply{Code: 7, Stdout: echo, Stderr: echo}
					case "create connection failure":
						return remotetest.Reply{Err: errors.New(echo)}
					default:
						return remotetest.Reply{Stdout: echo, Stderr: echo}
					}
				}, 0)
				m := &machine{runner: fake, cfg: remote.Config{Escalation: escalation, SudoPassword: "sudo-fixture"}}
				args := RcloneRemoteArgs{Remote: "archive", Type: "sftp", Settings: map[string]string{"host": setting}, Secrets: map[string]string{"pass": plain}}
				state, err := settleRemote(ctx, m, args)
				if (err == nil) != (outcome == "success") {
					t.Fatal("unexpected create result")
				}
				if err == nil {
					if state.Secrets["pass"] != plain || state.Settings["host"] != setting {
						t.Fatal("create changed the resource state")
					}
					got, readErr := readRemote(ctx, m, args.Remote, []string{"pass"}, state.Config)
					if readErr != nil || got == nil || got.Secrets["pass"] != plain {
						t.Fatal("read did not decode the configured secret")
					}
				}
				creates := 0
				for _, call := range fake.Calls {
					for _, secret := range []string{plain, obscured, setting} {
						if strings.Contains(call.Command, secret) || (err != nil && strings.Contains(err.Error(), secret)) {
							t.Fatal("a credential leaked into a command or error")
						}
					}
					if strings.Contains(call.Command, "/config/create") {
						creates++
						payload := call.Stdin
						if escalation == "sudo-password" {
							lines := strings.SplitN(payload, "\n", 3)
							if len(lines) != 3 || lines[0] != "sudo-fixture" || !strings.Contains(call.Command, lines[1]) {
								t.Fatal("missing sudo-password stdin framing")
							}
							payload = lines[2]
						}
						var request rcloneCreateRequest
						if json.Unmarshal([]byte(payload), &request) != nil || request.Parameters["pass"] != plain || request.Parameters["host"] != setting {
							t.Fatal("credentials did not reach rclone over stdin")
						}
					}
				}
				if creates != 1 {
					t.Fatalf("got %d config create calls, want 1", creates)
				}
			})
		}
	}
}

func TestRcloneCreateCommandWithRclone(t *testing.T) {
	for _, tool := range []string{"rclone", "curl", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			if os.Getenv("RCLONE_REQUIRE_TOOLS") != "" {
				t.Fatalf("%s is required: %v", tool, err)
			}
			t.Skipf("%s is unavailable", tool)
		}
	}

	config := filepath.Join(t.TempDir(), "rclone's config")
	var settings = map[string]string{"host": "fixture.example"}
	var secrets = map[string]string{"pass": "pass ' with quotes \\ and whitespace"}
	tmpDir := t.TempDir()
	t.Setenv("TMPDIR", tmpDir)

	for _, backend := range []string{"sftp", "missing-backend"} {
		t.Run(backend, func(t *testing.T) {
			payload, err := rcloneCreatePayload("archive", backend, settings, secrets)
			if err != nil {
				t.Fatal("could not make fixture")
			}
			cmd := exec.Command("sh", "-c", rcloneCreateCommand(config, "archive", backend, true))
			cmd.Stdin = strings.NewReader(string(payload))
			if err := cmd.Run(); (err == nil) != (backend == "sftp") {
				t.Fatalf("unexpected rclone create result: %v", err)
			}
			files, err := os.ReadDir(tmpDir)
			if err != nil || len(files) != 0 {
				t.Fatal("rclone's temporary directory was not cleaned up")
			}
		})
		if backend == "sftp" {
			dump, err := exec.Command("rclone", "--config", config, "config", "dump").Output()
			if err != nil {
				t.Fatal("could not read the rclone config")
			}
			stored := rclone.ParseDump(string(dump), "archive")
			plain, ok := rclone.Reveal(stored["pass"])
			if !ok || plain != secrets["pass"] || stored["host"] != settings["host"] {
				t.Fatal("rclone did not store the requested configuration")
			}
		}
	}
}

func TestRcloneRemoteReadDistinguishesDumpFailureFromAbsence(t *testing.T) {
	for _, tc := range []struct {
		name string
		dump remotetest.Reply
		want string
	}{
		{"command failure", remotetest.Reply{Code: 1, Stdout: "test-credential", Stderr: "could not parse line: test-credential"}, "failed (exit 1)"},
		{"malformed dump", remotetest.Reply{Stdout: "unexpected output"}, "invalid rclone config dump"},
		{"malformed remote", remotetest.Reply{Stdout: `{"backup": null}`}, "invalid remote in rclone config dump"},
		{"confirmed absence", remotetest.Reply{Stdout: `{"other": {"type":"sftp"}}`}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := remotetest.New().On(asRoot("rclone --config '"+rcloneDefaultConfig+"' config dump"), tc.dump)
			read, err := configuredServer(t, fake).Read(p.ReadRequest{ID: "backup", Urn: urnOf("RcloneRemote", "backup")})
			if tc.want != "" {
				requireStepError(t, err, tc.want)
				if strings.Contains(err.Error(), "test-credential") {
					t.Fatal("config dump error exposed a credential")
				}
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
		{"delete failed", "printf '%s' '{\"backup\":{\"type\":\"sftp\"}}'", "failed (exit 17)", true},
		{"dump failed", "echo 'config read denied' >&2; exit 1", "failed (exit 1)", false},
		{"already absent", "printf '{}'", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := shellStepRunner(t, map[string]string{"rclone": `
test "$1" = --config && test "$2" = /srv/rclone.conf || exit 99
case "$3 $4" in
  'config dump') ` + tc.script + ` ;;
  'config delete') echo 'config write denied' >&2; exit 17 ;;
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

func TestRcloneRemoteDeleteRedactsConnectionError(t *testing.T) {
	const config = "/srv/rclone.conf"
	const remote = "backup"
	const credential = "test-credential"
	command := "rclone --config " + shellQuote(config) + " config "
	fake := remotetest.New().
		On(asRoot(command+"dump"), remotetest.Reply{Stdout: `{"backup":{"type":"sftp"}}`}).
		On(asRoot(command+"delete "+shellQuote(remote)), remotetest.Reply{Err: errors.New(credential)})

	state := property.NewMap(map[string]property.Value{
		"config": property.New(config), "remote": property.New(remote), "type": property.New("sftp"),
		"settings": property.New(property.NewMap(nil)), "secrets": property.New(property.NewMap(nil)),
	})
	err := configuredServer(t, fake).Delete(p.DeleteRequest{ID: remote, Urn: urnOf("RcloneRemote", remote), Properties: state})
	requireStepError(t, err, "the connection failed")
	if strings.Contains(err.Error(), credential) {
		t.Fatal("config delete error exposed a credential")
	}
	if !fake.Ran("config delete") {
		t.Fatal("the config delete command did not run")
	}
}
