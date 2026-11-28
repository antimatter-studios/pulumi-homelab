//go:build integration

package homelab

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Integration tests of the package types against a real Debian 13 machine: a container, reached
// with `docker exec` as root. Run with: go test -tags integration -run Debian13 .
const packagesImage = "pulumi-homelab-packages:debian13"

const packagesDockerfile = `FROM debian:13
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git curl ca-certificates unzip xz-utils bzip2 >/dev/null
RUN git config --system user.email t@example.com && git config --system user.name t && git config --system init.defaultBranch main
`

type rootDockerRunner struct{ id string }

func (d rootDockerRunner) Run(ctx context.Context, command string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", d.id, "sh", "-c", command)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

func debian13(t *testing.T) *machine {
	t.Helper()
	build := exec.Command("docker", "build", "-q", "-t", packagesImage, "-")
	build.Stdin = strings.NewReader(packagesDockerfile)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", packagesImage, "sleep", "infinity").Output()
	if err != nil {
		t.Fatalf("docker run: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return &machine{runner: rootDockerRunner{id}, cfg: remote.Config{Escalation: "none", User: "root", Address: "debian13"}}
}

func sh(t *testing.T, m *machine, command string) string {
	t.Helper()
	out, err := m.must(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func exists(t *testing.T, m *machine, path string) bool {
	t.Helper()
	r, err := m.ask(t.Context(), "test -e "+shellQuote(path))
	if err != nil {
		t.Fatal(err)
	}
	return r.code == 0
}

// GitCheckout clones and pins the requested commit.
func TestDebian13GitCheckout(t *testing.T) {
	m := debian13(t)
	ctx := t.Context()
	sh(t, m, `git init -q --bare /srv/origin.git && git clone -q /srv/origin.git /tmp/seed && cd /tmp/seed &&
		echo one > a && printf 'ignored.log\n' > .gitignore && git add -A && git commit -qm one && git push -q origin main &&
		echo two > a && git commit -qam two && git push -q origin main`)
	first := strings.TrimSpace(sh(t, m, "git -C /tmp/seed rev-parse HEAD~1"))
	url := "file:///srv/origin.git"

	checkout := func(t *testing.T, path string) {
		t.Helper()
		state, err := settleCheckout(ctx, m, GitCheckoutArgs{URL: url, Path: path, Commit: first[:7]})
		if err != nil {
			t.Fatal(err)
		}
		if state.Head != first {
			t.Fatalf("head %s, want %s", state.Head, first)
		}
	}

	t.Run("clones at the pinned commit, detached", func(t *testing.T) {
		checkout(t, "/srv/pin/w")
		if out := sh(t, m, "git -C /srv/pin/w symbolic-ref -q HEAD || echo detached"); strings.TrimSpace(out) != "detached" {
			t.Fatalf("HEAD is %q", out)
		}
	})

	sh(t, m, "useradd -m checkout-owner")
	server := newServer(t, (&capture{runner: m.runner}).factory)
	if err := server.Configure(p.ConfigureRequest{Args: property.NewMap(with(homelabServerConfig(), "escalation", property.New("none")))}); err != nil {
		t.Fatal(err)
	}
	deleteResource := func(path string) error {
		return server.Delete(p.DeleteRequest{ID: path, Urn: urnOf("GitCheckout", "checkout"),
			Properties: property.NewMap(map[string]property.Value{"path": property.New(path), "url": property.New(url), "commit": property.New(first), "head": property.New(first)})})
	}
	for _, tc := range []struct{ name, change, finding string }{
		{"working tree", "echo changed > a", "Working tree changes:\n M a"},
		{"staged file", "echo changed > a && git add a", "Working tree changes:\nM  a"},
		{"untracked file", "echo changed > local.txt", "Working tree changes:\n?? local.txt"},
		{"detached commit", "echo changed > a && git commit -qam detached-work", "detached"},
		{"stash", "echo changed > a && git stash -q", "Stashes:\nstash@{0}"},
		{"unpushed branch", "git checkout -qb local && echo changed > a && git commit -qam local-work", "Unpushed branch commits:"},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			if tc.name == "detached commit" {
				t.Skip("blocked by #135: Delete does not check detached HEAD for local commits")
			}
			path := "/srv/w-" + strings.ReplaceAll(tc.name, " ", "-")
			checkout(t, path)
			sh(t, m, "cd "+shellQuote(path)+" && "+tc.change+" && chown -R checkout-owner:checkout-owner "+shellQuote(path))
			err := deleteResource(path)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), tc.finding) {
				t.Fatalf("got %v, want refusal naming %s and %q", err, path, tc.finding)
			}
			if !exists(t, m, path) {
				t.Fatal("checkout was removed")
			}
		})
	}

	t.Run("removes a clean checkout", func(t *testing.T) {
		path := "/srv/clean"
		checkout(t, path)
		sh(t, m, "echo ignored > /srv/clean/ignored.log && chown -R checkout-owner:checkout-owner /srv/clean")
		if err := deleteResource(path); err != nil {
			t.Fatal(err)
		}
		if exists(t, m, path) {
			t.Fatal("checkout is still present")
		}
	})
}

// Archive: installs from a verified archive with the declared owner, chowns rather than refetching
// when only the owner is wrong, and refuses a checksum that does not match.
func TestDebian13Archive(t *testing.T) {
	m := debian13(t)
	ctx := t.Context()
	sh(t, m, `useradd -m svc && mkdir -p /src/widget-1.0/bin && printf '#!/bin/sh\necho "widget 1.0.3"\n' > /src/widget-1.0/bin/widget &&
		chmod +x /src/widget-1.0/bin/widget && chown -R 4242:4242 /src/widget-1.0 && tar -C /src -czf /src/widget.tar.gz widget-1.0`)
	sum := strings.Fields(sh(t, m, "sha256sum /src/widget.tar.gz"))[0]
	owner := "svc"
	args := ArchiveArgs{
		Name: "widget", URL: strp("file:///src/widget.tar.gz"), Sha256: &sum, Prefix: "/opt/widget/1.0",
		Link: strp("/opt/widget/current"), Binary: strp("bin/widget"), HealthCommand: strp("{}/bin/widget"),
		VersionCommand: strp("{}/bin/widget"), Strip: intp(1), Owner: &owner, Group: &owner,
	}
	state, err := settleArchive(ctx, m, args, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Owner != "svc" || state.Group != "svc" || state.Version != "1.0.3" {
		t.Fatalf("state %+v", state)
	}
	if got := strings.TrimSpace(sh(t, m, "stat -c '%U' /opt/widget/1.0/bin/widget; readlink /opt/widget/current")); got != "svc\n/opt/widget/1.0" {
		t.Fatalf("got %q", got)
	}

	// only the owner wrong: a chown, and the tree is not replaced
	sh(t, m, "chown -R root:root /opt/widget/1.0 && touch /opt/widget/1.0/updated-by-itself")
	previous := state.source()
	if state, err = settleArchive(ctx, m, args, &previous); err != nil || state.Owner != "svc" {
		t.Fatalf("%+v %v", state, err)
	}
	if !exists(t, m, "/opt/widget/1.0/updated-by-itself") {
		t.Fatal("refetched rather than chowned")
	}

	// a checksum that does not match never reaches the filesystem
	bad := args
	bad.Prefix, bad.Sha256, bad.Link = "/opt/bad", strp(strings.Repeat("0", 64)), nil
	if _, err := settleArchive(ctx, m, bad, nil); err == nil || exists(t, m, "/opt/bad") {
		t.Fatalf("installed a mismatched archive: %v", err)
	}

	sh(t, m, archiveDeleteCommand("/opt/widget/1.0", "/opt/widget/current"))
	if exists(t, m, "/opt/widget/1.0") || exists(t, m, "/opt/widget/current") {
		t.Fatal("delete left things behind")
	}
}

// DebPackage and AptPackage/AptPackages through a real dpkg and apt.
func TestDebian13Packages(t *testing.T) {
	m := debian13(t)
	ctx := t.Context()
	sh(t, m, `mkdir -p /build/demo/DEBIAN /build/demo/usr/bin /build/demo/etc && printf '#!/bin/sh\necho demo\n' > /build/demo/usr/bin/demo &&
		chmod +x /build/demo/usr/bin/demo && printf 'Package: pulumi-demo\nVersion: 1.2.3\nArchitecture: all\nMaintainer: t <t@example.com>\nDescription: demo\n' > /build/demo/DEBIAN/control &&
		printf '/etc/pulumi-demo.conf\n' > /build/demo/DEBIAN/conffiles &&
		printf 'default one\n' > /build/demo/etc/pulumi-demo.conf &&
		dpkg-deb -b /build/demo /build/demo.deb >/dev/null`)
	sum := strings.Fields(sh(t, m, "sha256sum /build/demo.deb"))[0]

	t.Run("keeps local conffile edits on upgrade and removes the conffile on purge", func(t *testing.T) {
		version, err := installDeb(ctx, m, DebPackageArgs{Name: "pulumi-demo", URL: "file:///build/demo.deb", Sha256: sum})
		if err != nil || version != "1.2.3" {
			t.Fatalf("%q %v", version, err)
		}
		if _, err := installDeb(ctx, m, DebPackageArgs{Name: "pulumi-other", URL: "file:///build/demo.deb", Sha256: sum}); err == nil ||
			!strings.Contains(err.Error(), "dpkg has no package named pulumi-other") {
			t.Fatalf("got %v", err)
		}
		if got := sh(t, m, "cat /etc/pulumi-demo.conf"); got != "default one\n" {
			t.Fatalf("installed conffile: %q", got)
		}
		sh(t, m, `printf 'local edit\n' > /etc/pulumi-demo.conf &&
			printf 'default two\n' > /build/demo/etc/pulumi-demo.conf &&
			sed -i 's/Version: 1.2.3/Version: 1.2.4/' /build/demo/DEBIAN/control &&
			dpkg-deb -b /build/demo /build/demo-v2.deb >/dev/null`)
		sumV2 := strings.Fields(sh(t, m, "sha256sum /build/demo-v2.deb"))[0]
		version, err = installDeb(ctx, m, DebPackageArgs{Name: "pulumi-demo", URL: "file:///build/demo-v2.deb", Sha256: sumV2})
		if err != nil || version != "1.2.4" {
			t.Fatalf("upgrade: %q, %v", version, err)
		}
		if got := sh(t, m, "cat /etc/pulumi-demo.conf"); got != "local edit\n" {
			t.Fatalf("upgrade overwrote the local conffile: %q", got)
		}
		server := newServer(t, (&capture{runner: m.runner}).factory)
		if err := server.Configure(p.ConfigureRequest{Args: property.NewMap(with(homelabServerConfig(), "escalation", property.New("none")))}); err != nil {
			t.Fatal(err)
		}
		if err := server.Delete(p.DeleteRequest{ID: "pulumi-demo", Urn: urnOf("AptPackage", "demo"),
			Properties: property.NewMap(map[string]property.Value{"name": property.New("pulumi-demo"), "update": property.New(false), "version": property.New(version), "manual": property.New(true)})}); err != nil {
			t.Fatal(err)
		}
		if exists(t, m, "/etc/pulumi-demo.conf") {
			t.Fatal("purge left the conffile behind")
		}
		if _, ok, _ := readPackage(ctx, m, "pulumi-demo"); ok {
			t.Fatal("still installed")
		}
	})

	t.Run("refuses a .deb whose checksum does not match", func(t *testing.T) {
		if _, err := installDeb(ctx, m, DebPackageArgs{Name: "pulumi-demo", URL: "file:///build/demo.deb", Sha256: strings.Repeat("0", 64)}); err == nil {
			t.Fatal("installed")
		}
	})

	t.Run("AptPackages installs, marks manual, and purges a declared-absent package", func(t *testing.T) {
		update := true
		state, err := settlePackages(ctx, m, AptPackagesArgs{Present: []string{"hello"}, Update: &update})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := state.Installed["hello"]; !ok || len(state.Missing) != 0 || len(state.Auto) != 0 {
			t.Fatalf("state %+v", state)
		}

		// an auto-marked declared package is marked manual
		sh(t, m, "apt-mark auto hello >/dev/null")
		if state, err = settlePackages(ctx, m, AptPackagesArgs{Present: []string{"hello"}, Absent: []string{"hello-traditional"}}); err != nil || len(state.Auto) != 0 {
			t.Fatalf("%+v %v", state, err)
		}
		if manual, _ := readManual(ctx, m, "hello"); !manual {
			t.Fatal("not manual")
		}
		// a declared-absent package with no dependents is purged
		if _, err := settlePackages(ctx, m, AptPackagesArgs{Absent: []string{"hello"}}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := readPackage(ctx, m, "hello"); ok {
			t.Fatal("hello still installed")
		}
	})

	t.Run("AptPackage installs one package and marks it manual", func(t *testing.T) {
		version, err := installAptPackage(ctx, m, AptPackageArgs{Name: "hello"})
		if err != nil || version == "" {
			t.Fatalf("%q %v", version, err)
		}
		if manual, err := markManual(ctx, m, "hello"); err != nil || !manual {
			t.Fatalf("%v %v", manual, err)
		}
	})
}

func TestDebian13OfficialInstall(t *testing.T) {
	m := debian13(t)
	sh(t, m, "useradd -m vendor-test")
	s := newServer(t, (&capture{runner: m.runner}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(with(homelabServerConfig(), "escalation", property.New("none")))}); err != nil {
		t.Fatal(err)
	}
	const home = "/home/vendor-test"
	const bin = home + "/.local/bin/demo"
	urn := urnOf("OfficialInstall", "demo")
	inputs := property.NewMap(map[string]property.Value{
		"account": property.New("vendor-test"), "home": property.New(home), "bin": property.New(bin),
		"installer": property.New(`test "$(id -un)" = vendor-test && test "$PWD" = "$HOME" &&
			mkdir -p .local/bin && printf '#!/bin/sh\nprintf "demo 1.2.3\\n"\n' > .local/bin/demo &&
			chmod +x .local/bin/demo`),
	})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	if err != nil || created.ID != "vendor-test:"+bin || created.Properties.Get("version").AsString() != "demo 1.2.3" {
		t.Fatalf("install: %+v, %v", created, err)
	}
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: urn, Properties: created.Properties, Inputs: inputs})
	if err != nil || read.ID != created.ID || read.Properties.Get("version").AsString() != "demo 1.2.3" {
		t.Fatalf("read: %+v, %v", read, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("diff after refresh: %+v, %v", diff, err)
	}
	for field, changed := range map[string]string{"account": "someone-else", "bin": home + "/different-bin"} {
		t.Run("replaces on "+field, func(t *testing.T) {
			diff, err := s.Diff(p.DiffRequest{ID: read.ID, Urn: urn, State: read.Properties, Inputs: inputs.Set(field, property.New(changed))})
			if err != nil || !diff.HasChanges || diff.DeleteBeforeReplace || len(diff.DetailedDiff) != 1 || diff.DetailedDiff[field].Kind != p.UpdateReplace {
				t.Fatalf("diff: %+v, %v", diff, err)
			}
		})
	}
	sh(t, m, "rm "+bin)
	if gone, err := s.Read(p.ReadRequest{ID: read.ID, Urn: urn, Properties: read.Properties, Inputs: inputs}); err != nil || gone.ID != "" {
		t.Fatalf("read after removal: %+v, %v", gone, err)
	}
}
