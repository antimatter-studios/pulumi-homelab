//go:build integration

package homelab

import (
	"os/exec"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
)

// Integration tests against a real Debian 13 shell, stat and sudo in a container, so the reads are
// tested against the tools rather than against somebody's memory of them.
// Run with: go test -tags integration ./...
//
// The image is the transport's Debian 13 sshd image (user chris, passwordless sudo); commands run
// in it through docker exec as chris, with their stdin, which is all a remote.Runner promises.
const fileTestImage = "pulumi-homelab-transport-sshd:debian13"

func dockerOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func startDebian(t *testing.T) dockerRunner {
	t.Helper()
	dockerOut(t, "build", "-q", "-t", fileTestImage, "transport/testdata/debian13-sshd")
	id := dockerOut(t, "run", "-d", "--rm", fileTestImage, "sleep", "infinity")
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return dockerRunner{id: id}
}

func onMachine(t *testing.T, d dockerRunner, command string) string {
	t.Helper()
	out, errb, code, err := d.Run(t.Context(), command, nil)
	if err != nil || code != 0 {
		t.Fatalf("%s: %v exit %d %s", command, err, code, errb)
	}
	return string(out)
}

// Ported from src/local.test.ts "reading a file the machine actually has".
func TestReadFileRealShell(t *testing.T) {
	d := startDebian(t)
	m := &machine{runner: d, cfg: remote.Config{Escalation: "none"}}
	ctx := t.Context()
	where := strings.TrimSpace(onMachine(t, d, "mktemp -d"))

	t.Run("reads back what was written, with the mode in the shape the code writes it", func(t *testing.T) {
		path := where + "/written.conf"
		if err := writeManagedFile(ctx, m, wantedFile{path: path, content: "a=b\n", mode: "0640"}); err != nil {
			t.Fatal(err)
		}
		found, err := readManagedFile(ctx, m, path)
		if err != nil || found == nil {
			t.Fatalf("%+v %v", found, err)
		}
		if found.Content != "a=b\n" {
			t.Fatalf("content %q", found.Content)
		}
		// stat says 640 where the code says 0640, and comparing those is drift on every refresh
		if found.Mode != "0640" {
			t.Fatalf("mode %q", found.Mode)
		}
	})

	t.Run("says nothing rather than something wrong about a file that is not there", func(t *testing.T) {
		found, err := readManagedFile(ctx, m, where+"/absent")
		if err != nil || found != nil {
			t.Fatalf("%+v %v", found, err)
		}
	})

	t.Run("keeps a dollar sign, which a here-document would otherwise expand away", func(t *testing.T) {
		path := where + "/unit.conf"
		if err := writeManagedFile(ctx, m, wantedFile{path: path, content: "ExecStart=/bin/x $MAINPID\n", mode: "0644"}); err != nil {
			t.Fatal(err)
		}
		found, err := readManagedFile(ctx, m, path)
		if err != nil || found == nil || !strings.Contains(found.Content, "$MAINPID") {
			t.Fatalf("%+v %v", found, err)
		}
	})
}

// More than 1 MB, written as root through sudo, arrives byte-exact with its newline added, mode
// and owner set, no staged file left behind, and reads back byte for byte.
func TestWriteLargeFileRealShell(t *testing.T) {
	d := startDebian(t)
	ctx := t.Context()
	content := strings.Repeat("<p>'quotes' $HOME \\ é 🇪🇸</p>", 40_000) // no final newline
	for _, escalation := range []string{"sudo", "sudo-password"} {
		t.Run(escalation, func(t *testing.T) {
			// sudo-password against a NOPASSWD sudoers is the case where sudo never reads the
			// password: were it on the content's stdin, it would end up in the file
			m := &machine{runner: d, cfg: remote.Config{Escalation: escalation, SudoPassword: "pw"}}
			w := wantedFile{path: "/srv/" + escalation + "/deep/page.html", content: content, mode: "0640", owner: "chris", group: "root"}
			if err := writeManagedFile(ctx, m, w); err != nil {
				t.Fatal(err)
			}
			if got := strings.Fields(onMachine(t, d, "sha256sum "+w.path))[0]; got != digestOf([]byte(content+"\n")) {
				t.Fatalf("on disk %s, want %s", got, digestOf([]byte(content+"\n")))
			}
			if got := onMachine(t, d, "stat -c '%a %U %G' "+w.path); got != "640 chris root\n" {
				t.Fatalf("stat %q", got)
			}
			if got := onMachine(t, d, "ls -A /srv/"+escalation+"/deep; ls -A /tmp"); got != "page.html\n" {
				t.Fatalf("left behind: %q", got)
			}
			found, err := readManagedFile(ctx, m, w.path)
			if err != nil || found == nil || found.Content != content+"\n" {
				t.Fatalf("read back %v", err)
			}
		})
	}
}

// The whole resource through the gRPC surface against the real machine: create, refresh, a hand
// edit seen as drift, update, delete.
func TestManagedAndProtectedFileRealShell(t *testing.T) {
	d := startDebian(t)
	s := newServer(t, (&capture{runner: d}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	const path = "/etc/demo/unit.conf"
	inputs := property.NewMap(map[string]property.Value{"path": property.New(path), "content": property.New("A=$B\n")})
	created, err := s.Create(p.CreateRequest{Urn: fileURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if got := onMachine(t, d, "stat -c '%a %U %G' "+path+"; cat "+path); got != "644 root root\nA=$B\n" {
		t.Fatalf("on disk %q", got)
	}
	read, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN, Properties: created.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: read.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after refresh %+v %v", diff, err)
	}
	onMachine(t, d, "echo edited | sudo tee "+path+" >/dev/null")
	drifted, err := s.Read(p.ReadRequest{ID: path, Urn: fileURN, Properties: read.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, _ := s.Diff(p.DiffRequest{ID: path, Urn: fileURN, State: drifted.Properties, Inputs: inputs}); !diff.HasChanges {
		t.Fatal("a hand edit was not drift")
	}
	if err := s.Delete(p.DeleteRequest{ID: path, Urn: fileURN, Properties: drifted.Properties}); err == nil ||
		!strings.Contains(err.Error(), "the file changed") || onMachine(t, d, "cat "+path) != "edited\n" {
		t.Fatalf("delete of drifted file: %v", err)
	}
	updated, err := s.Update(p.UpdateRequest{ID: path, Urn: fileURN, State: drifted.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if got := onMachine(t, d, "cat "+path); got != "A=$B\n" {
		t.Fatalf("after update %q", got)
	}
	if err := s.Delete(p.DeleteRequest{ID: path, Urn: fileURN, Properties: updated.Properties}); err != nil {
		t.Fatal(err)
	}
	if got := onMachine(t, d, "ls -A /etc/demo"); got != "" {
		t.Fatalf("delete left %q", got)
	}

	// ProtectedFile: binary material from an exec source lands byte-exact, private from the first byte
	const secretPath = "/home/chris/.config/token"
	source := property.New(map[string]property.Value{"kind": property.New("exec"),
		"argv": property.New([]property.Value{property.New("printf"), property.New(`\000\377secret`)})})
	pin := property.NewMap(map[string]property.Value{"path": property.New(secretPath), "source": source,
		"owner": property.New("chris"), "group": property.New("chris"), "enforce": property.New("always")})
	made, err := s.Create(p.CreateRequest{Urn: protectedURN, Properties: pin})
	if err != nil {
		t.Fatal(err)
	}
	if got := onMachine(t, d, "stat -c '%a %U %G' "+secretPath+"; od -An -c "+secretPath); !strings.Contains(got, "600 chris chris") || !strings.Contains(got, `\0 377   s   e   c   r   e   t`) {
		t.Fatalf("on disk %q", got)
	}
	again, err := s.Read(p.ReadRequest{ID: secretPath, Urn: protectedURN, Properties: made.Properties, Inputs: pin})
	if err != nil || again.Properties.Get("digest").AsString() != made.Properties.Get("digest").AsString() {
		t.Fatalf("read %+v %v", again, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: secretPath, Urn: protectedURN, State: again.Properties, Inputs: pin}); err != nil || diff.HasChanges {
		t.Fatalf("diff %+v %v", diff, err)
	}

	t.Run("once preserves a service rotation", func(t *testing.T) {
		const oncePath = "/home/chris/.config/service-token"
		once := pin.Set("enforce", property.New("once")).Set("path", property.New(oncePath))
		created, err := s.Create(p.CreateRequest{Urn: protectedURN, Properties: once})
		if err != nil || !created.Properties.Get("writtenDigest").IsSecret() {
			t.Fatalf("create once: %v", err)
		}
		onMachine(t, d, "printf rotated > "+oncePath)
		refreshed, err := s.Read(p.ReadRequest{ID: oncePath, Urn: protectedURN, Inputs: once, Properties: created.Properties})
		if err != nil || refreshed.Properties.Get("digest").AsString() != seeded {
			t.Fatalf("read once: %v", err)
		}
		if err := s.Delete(p.DeleteRequest{ID: oncePath, Urn: protectedURN, Properties: refreshed.Properties}); err == nil ||
			!strings.Contains(err.Error(), "the file changed") || onMachine(t, d, "cat "+oncePath) != "rotated" {
			t.Fatalf("delete of rotated file: %v", err)
		}
		onMachine(t, d, `printf '\000\377secret' > `+oncePath)
		if err := s.Delete(p.DeleteRequest{ID: oncePath, Urn: protectedURN, Properties: refreshed.Properties}); err != nil {
			t.Fatal(err)
		}
		onMachine(t, d, "test ! -e "+oncePath)
	})
}
