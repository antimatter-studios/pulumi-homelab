package homelab

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// The SshdConfig change script runs for real here, in this machine's /bin/sh, against a
// configuration tree in a temporary directory. What only a machine has is stood in for by scripts
// on PATH: sudo runs the command, sshd parses the tree (following Include) and reports what it
// resolves, systemctl reloads by re-reading that, ss lists what is listening, and bash answers the
// banner probe. Keywords in the configuration steer the fake sshd: BROKEN fails sshd -t, XDie kills
// it on reload, XNoListen leaves it running with nothing listening, XNoBanner makes it silent.
// The files fail-live-t and fail-reload in the state directory fail the next live sshd -t or
// reload, once. Every fake call is logged, so the tests can see the order things happened in.

var sshdURN = presource.URN("urn:pulumi:s::p::homelab:index:SshdConfig::tunnel")

type sshdBox struct {
	root, state, log, config, directory string
}

const sshdShim = `#!/bin/sh
[ -n "$SHIM_QUIET" ] || echo "sshd $*" >> "$SHIM_STATE/log"
file=$SHIM_CONFIG; mode=
while [ $# -gt 0 ]; do case $1 in -t) mode=t;; -T) mode=T;; -f) shift; file=$1;; esac; shift; done
expand() {
  while IFS= read -r line || [ -n "$line" ]; do
    set -- $line
    case $1 in
      [Ii]nclude) shift; for g in "$@"; do for f in $g; do [ -f "$f" ] && expand < "$f"; done; done;;
      *) printf '%s\n' "$line";;
    esac
  done
}
[ -f "$file" ] || { echo "$file: No such file or directory" >&2; exit 255; }
effective=$(expand < "$file")
if [ "$mode" = t ]; then
  if printf '%s\n' "$effective" | grep -q BROKEN; then echo "$file line 2: Bad configuration option: BROKEN" >&2; exit 255; fi
  if [ "$file" = "$SHIM_CONFIG" ] && [ -e "$SHIM_STATE/fail-live-t" ]; then rm -f "$SHIM_STATE/fail-live-t"; echo "live check failed" >&2; exit 255; fi
  exit 0
fi
printf '%s\n' "$effective" | awk '
  /^[[:space:]]*(#|$)/ { next }
  { k = tolower($1); v = $2; for (i = 3; i <= NF; i++) v = v " " $i
    if (k == "port") { ports = ports " " v; next }
    if (!(k in seen)) { seen[k] = 1; print k " " v } }
  END { if (ports == "") ports = " 22"; n = split(ports, p, " "); for (i = 1; i <= n; i++) print "listenaddress 0.0.0.0:" p[i] }'
`

const systemctlShim = `#!/bin/sh
echo "systemctl $*" >> "$SHIM_STATE/log"
s=$SHIM_STATE
come_up() {
  effective=$(SHIM_QUIET=1 sshd -T)
  rm -f "$s/listening" "$s/silent"
  if printf '%s\n' "$effective" | grep -q '^xdie '; then echo no > "$s/active"; return 0; fi
  echo yes > "$s/active"
  printf '%s\n' "$effective" | grep -q '^xnolisten ' || printf '%s\n' "$effective" | awk '$1 == "listenaddress" { n = split($2, a, ":"); print a[n] }' > "$s/listening"
  printf '%s\n' "$effective" | grep -q '^xnobanner ' && touch "$s/silent"
  return 0
}
case $1 in
  is-active) for u; do :; done; [ "$u" = ssh.service ] && [ "$(cat "$s/active")" = yes ] && exit 0; exit 3;;
  reload) [ "$(cat "$s/active")" = yes ] || exit 1
    if [ -e "$s/fail-reload" ]; then rm -f "$s/fail-reload"; exit 1; fi
    come_up;;
  start) come_up;;
  *) exit 1;;
esac
`

const ssShim = `#!/bin/sh
echo "ss $*" >> "$SHIM_STATE/log"
echo "State Recv-Q Send-Q Local-Address:Port Peer-Address:Port"
[ -f "$SHIM_STATE/listening" ] || exit 0
while read -r port; do echo "LISTEN 0 128 0.0.0.0:$port 0.0.0.0:*"; done < "$SHIM_STATE/listening"
`

// bash stands in only for the banner probe: bash -c PROBE _ HOST PORT.
const bashShim = `#!/bin/sh
echo "probe $4:$5" >> "$SHIM_STATE/log"
[ -f "$SHIM_STATE/listening" ] && grep -qx "$5" "$SHIM_STATE/listening" && [ ! -e "$SHIM_STATE/silent" ]
`

func newSshdBox(t *testing.T) *sshdBox {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := t.TempDir()
	b := &sshdBox{root: root, state: filepath.Join(root, "state"), config: filepath.Join(root, "etc/ssh/sshd_config"),
		directory: filepath.Join(root, "etc/ssh/sshd_config.d")}
	b.log = filepath.Join(b.state, "log")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{bin, b.state, b.directory} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shims := map[string]string{
		"sudo":      "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != sh ]; do shift; done\nexec \"$@\"\n",
		"sshd":      sshdShim,
		"systemctl": systemctlShim,
		"ss":        ssShim,
		"bash":      bashShim,
		"timeout":   "#!/bin/sh\nshift\nexec \"$@\"\n",
		"sleep":     "#!/bin/sh\nexit 0\n",
		"flock":     "#!/bin/sh\nexit 0\n",
	}
	for name, body := range shims {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.write(t, b.config, "Include "+b.directory+"/*.conf\nPermitRootLogin no\n")
	b.write(t, filepath.Join(b.state, "active"), "yes\n")
	b.write(t, filepath.Join(b.state, "listening"), "22\n")
	b.write(t, b.log, "")
	return b
}

func (b *sshdBox) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (b *sshdBox) touch(t *testing.T, name string) { b.write(t, filepath.Join(b.state, name), "") }

func (b *sshdBox) dropIn() (string, bool) {
	content, err := os.ReadFile(filepath.Join(b.directory, "90-tunnel.conf"))
	return string(content), err == nil
}

func (b *sshdBox) calls(t *testing.T) []string {
	t.Helper()
	log, err := os.ReadFile(b.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(log)), "\n")
}

func (b *sshdBox) resetLog(t *testing.T) { b.write(t, b.log, "") }

func (b *sshdBox) active(t *testing.T) bool {
	t.Helper()
	got, _ := os.ReadFile(filepath.Join(b.state, "active"))
	return strings.TrimSpace(string(got)) == "yes"
}

// Run implements remote.Runner by running the command in this machine's shell, shims first.
func (b *sshdBox) Run(ctx context.Context, command string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(b.root, "bin")+":"+os.Getenv("PATH"),
		"SHIM_STATE="+b.state, "SHIM_CONFIG="+b.config)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// server is the provider configured to reach this box, escalating with sudo -n as on a machine.
func (b *sshdBox) server(t *testing.T) interface {
	Create(p.CreateRequest) (p.CreateResponse, error)
	Update(p.UpdateRequest) (p.UpdateResponse, error)
	Delete(p.DeleteRequest) error
} {
	t.Helper()
	s := newServer(t, (&capture{runner: b}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	return s
}

func (b *sshdBox) inputs(unchecked map[string]string) property.Map {
	values := map[string]property.Value{}
	for k, v := range unchecked {
		values[k] = property.New(v)
	}
	return property.NewMap(map[string]property.Value{
		"file": property.New("90-tunnel"), "directory": property.New(b.directory), "config": property.New(b.config),
		"settings":  property.New(property.NewMap(map[string]property.Value{"x11Forwarding": property.New(false)})),
		"unchecked": property.New(property.NewMap(values)),
	})
}

// indexOf is the position of the first logged call that starts with prefix and contains part.
func indexOf(calls []string, prefix, part string) int {
	for i, c := range calls {
		if strings.HasPrefix(c, prefix) && strings.Contains(c, part) {
			return i
		}
	}
	return -1
}

func TestSshdChangeInstallsInOrder(t *testing.T) {
	b := newSshdBox(t)
	s := b.server(t)
	created, err := s.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if content, ok := b.dropIn(); !ok || !strings.Contains(content, "X11Forwarding no") {
		t.Fatalf("drop-in %q %v", content, ok)
	}
	if got := created.Properties.Get("effective").AsMap().Get("x11forwarding").AsString(); got != "no" {
		t.Errorf("effective x11forwarding %q", got)
	}
	calls := b.calls(t)
	mirror := indexOf(calls, "sshd -t -f ", "/root"+b.config)
	live := indexOf(calls, "sshd -t -f "+b.config, "")
	reload := indexOf(calls, "systemctl reload ssh.service", "")
	if mirror < 0 || live <= mirror || reload <= live {
		t.Fatalf("want the copy checked, then the live tree, then a reload:\n%s", strings.Join(calls, "\n"))
	}
	after := calls[reload+1:]
	if indexOf(after, "systemctl is-active", "ssh.service") < 0 || indexOf(after, "ss -ltn", "") < 0 || indexOf(after, "probe 127.0.0.1:22", "") < 0 {
		t.Fatalf("no health check after the reload:\n%s", strings.Join(after, "\n"))
	}
	if indexOf(calls, "systemctl restart", "") >= 0 || indexOf(calls, "systemctl start", "") >= 0 {
		t.Fatalf("restarted sshd:\n%s", strings.Join(calls, "\n"))
	}
	entries, _ := os.ReadDir(b.directory)
	if len(entries) != 1 {
		t.Fatalf("left files behind in %s: %v", b.directory, entries)
	}

	// an identical declaration touches nothing
	b.resetLog(t)
	if _, err := s.Update(p.UpdateRequest{ID: created.ID, Urn: sshdURN, State: created.Properties, Inputs: b.inputs(nil)}); err != nil {
		t.Fatal(err)
	}
	if indexOf(b.calls(t), "systemctl reload", "") >= 0 {
		t.Fatal("reloaded for an unchanged drop-in")
	}

	// delete takes the same path
	b.resetLog(t)
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: sshdURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.dropIn(); ok {
		t.Fatal("drop-in still there")
	}
	calls = b.calls(t)
	mirror, live, reload = indexOf(calls, "sshd -t -f ", "/root"+b.config), indexOf(calls, "sshd -t -f "+b.config, ""), indexOf(calls, "systemctl reload ssh.service", "")
	if mirror < 0 || live <= mirror || reload <= live || indexOf(calls[reload+1:], "probe", "") < 0 {
		t.Fatalf("delete did not check, reload and verify:\n%s", strings.Join(calls, "\n"))
	}

	// a second delete has nothing to do
	b.resetLog(t)
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: sshdURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if indexOf(b.calls(t), "systemctl reload", "") >= 0 {
		t.Fatal("reloaded for a drop-in that was already gone")
	}
}

func TestSshdChangeRollsBack(t *testing.T) {
	const previous = "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nX11Forwarding no\n"
	cases := []struct {
		name      string
		unchecked map[string]string
		knob      string
		existing  bool
		want      []string
		reloads   bool
	}{
		{name: "an invalid setting is refused before anything changes", unchecked: map[string]string{"BROKEN": "yes"}, existing: true,
			want: []string{"sshd -t rejects the configuration with this change", "Bad configuration option: BROKEN", "nothing was changed"}},
		{name: "a live sshd -t failure restores the previous file", unchecked: map[string]string{"MaxAuthTries": "3"}, knob: "fail-live-t", existing: true,
			want: []string{"sshd -t rejects the live configuration", "Rolled back: restored the previous", "sshd is healthy again"}},
		{name: "a live sshd -t failure removes a file that was not there", unchecked: map[string]string{"MaxAuthTries": "3"}, knob: "fail-live-t",
			want: []string{"sshd -t rejects the live configuration", "Rolled back: removed"}},
		{name: "a failed reload restores the previous file", unchecked: map[string]string{"MaxAuthTries": "3"}, knob: "fail-reload", existing: true,
			want: []string{"systemctl reload ssh.service failed", "Rolled back: restored the previous", "sshd is healthy again"}, reloads: true},
		{name: "sshd dying on reload is restored and started again", unchecked: map[string]string{"XDie": "yes"}, existing: true,
			want: []string{"sshd is not healthy after the reload: neither ssh nor sshd is active", "Rolled back: restored the previous", "sshd is healthy again"}, reloads: true},
		{name: "nothing listening after the reload rolls back", unchecked: map[string]string{"XNoListen": "yes"},
			want: []string{"nothing listens on port 22", "Rolled back: removed", "sshd is healthy again"}, reloads: true},
		{name: "no banner after the reload rolls back", unchecked: map[string]string{"XNoBanner": "yes"}, existing: true,
			want: []string{"no listen address answered with an SSH-2.0 banner", "Rolled back: restored the previous", "sshd is healthy again"}, reloads: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := newSshdBox(t)
			if c.existing {
				b.write(t, filepath.Join(b.directory, "90-tunnel.conf"), previous)
			}
			if c.knob != "" {
				b.touch(t, c.knob)
			}
			srv := b.server(t)
			_, err := srv.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(c.unchecked)})
			if err == nil {
				t.Fatal("the change went through")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q:\n%v", w, err)
				}
			}
			content, ok := b.dropIn()
			if c.existing && (!ok || content != previous) {
				t.Errorf("previous drop-in not restored: %q %v", content, ok)
			}
			if !c.existing && ok {
				t.Errorf("new drop-in left behind: %q", content)
			}
			entries, _ := os.ReadDir(b.directory)
			if want := map[bool]int{true: 1, false: 0}[c.existing]; len(entries) != want {
				t.Errorf("files in %s: %v", b.directory, entries)
			}
			if !b.active(t) {
				t.Error("sshd left dead")
			}
			calls := b.calls(t)
			if reloaded := indexOf(calls, "systemctl reload", "") >= 0; reloaded != c.reloads && c.knob != "fail-live-t" {
				t.Errorf("reloaded %v, want %v:\n%s", reloaded, c.reloads, strings.Join(calls, "\n"))
			}
		})
	}
}

func TestSshdDeleteRollsBack(t *testing.T) {
	const keep = "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nX11Forwarding no\n"
	b := newSshdBox(t)
	b.write(t, filepath.Join(b.directory, "90-tunnel.conf"), keep)
	b.touch(t, "fail-reload")
	srv := b.server(t)
	state := property.NewMap(map[string]property.Value{
		"file": property.New("90-tunnel"), "directory": property.New(b.directory), "config": property.New(b.config),
		"rendered": property.New(property.NewMap(map[string]property.Value{})), "match": property.New(property.NewMap(map[string]property.Value{})),
		"effective": property.New(property.NewMap(map[string]property.Value{})), "overridden": property.New([]property.Value{}),
	})
	err := srv.Delete(p.DeleteRequest{ID: "90-tunnel", Urn: sshdURN, Properties: state})
	if err == nil || !strings.Contains(err.Error(), "could not remove the sshd drop-in") || !strings.Contains(err.Error(), "Rolled back: restored the previous") {
		t.Fatalf("%v", err)
	}
	if content, ok := b.dropIn(); !ok || content != keep {
		t.Fatalf("drop-in not restored: %q %v", content, ok)
	}
}

func TestSshdChangeRefusesWhenSshdIsAlreadyUnhealthy(t *testing.T) {
	b := newSshdBox(t)
	b.write(t, filepath.Join(b.state, "active"), "no\n")
	srv := b.server(t)
	_, err := srv.Create(p.CreateRequest{Urn: sshdURN, Properties: b.inputs(nil)})
	if err == nil || !strings.Contains(err.Error(), "sshd is not healthy before the change") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("%v", err)
	}
	if _, ok := b.dropIn(); ok {
		t.Fatal("installed anyway")
	}
}

// The script on its own: what it asks of the machine, in order, without a shell.
func TestSshdChangeCommand(t *testing.T) {
	content := "# header\nX11Forwarding no\n"
	install := sshdChangeCommand("/etc/ssh/sshd_config.d/90-x.conf", "/etc/ssh/sshd_config.d", "/etc/ssh/sshd_config", &content)
	order := []string{
		"target='/etc/ssh/sshd_config.d/90-x.conf'; dir='/etc/ssh/sshd_config.d'; config='/etc/ssh/sshd_config'; mode=install",
		"cat > \"$work/candidate\" <<'PULUMI_EOF'\n# header\nX11Forwarding no\nPULUMI_EOF\n",
		`cp -a "$r/." "$mirror$r/"`,
		`out=$(sshd -t -f "$mirror$config" 2>&1) || refuse`,
		`settle || refuse "sshd is not healthy before the change`,
		`put "$work/candidate" || refuse`,
		`out=$(sshd -t -f "$config" 2>&1) || rollback`,
		`reload_sshd || rollback`,
		`settle || rollback`,
	}
	at := 0
	for _, step := range order {
		i := strings.Index(install[at:], step)
		if i < 0 {
			t.Fatalf("missing, or out of order: %q", step)
		}
		at += i + len(step)
	}
	for _, banned := range []string{"systemctl restart", "|| true"} {
		if strings.Contains(install, banned) {
			t.Errorf("script contains %q", banned)
		}
	}
	remove := sshdChangeCommand("/etc/ssh/sshd_config.d/90-x.conf", "/etc/ssh/sshd_config.d", "/etc/ssh/sshd_config", nil)
	if !strings.Contains(remove, "mode=remove") || strings.Contains(remove, "PULUMI_EOF") {
		t.Errorf("remove script:\n%s", remove)
	}
}
