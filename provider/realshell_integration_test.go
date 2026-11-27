//go:build integration

package homelab

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Integration tests against a real shell in a Debian 13 container, the commands reaching it as they
// would reach a machine: as the user chris, escalating with sudo -n.
// Run with: go test -tags integration ./...
const debianShellImage = "pulumi-homelab-provider-shell:debian13"

// dockerRunner is a remote.Runner that runs each command in the container with docker exec.
type dockerRunner struct{ id string }

func (d dockerRunner) Run(ctx context.Context, command string, stdin []byte) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", "-u", "chris", d.id, "sh", "-c", command)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(stdin), &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// startDebianShell starts a container and returns a runner into it, removed when the test ends.
func startDebianShell(t *testing.T) dockerRunner {
	t.Helper()
	if out, err := exec.Command("docker", "build", "-q", "-t", debianShellImage, "testdata/debian13-shell").CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", debianShellImage).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", id).Run() })
	return dockerRunner{id: id}
}

// sh runs a setup command in the container as root, failing the test if it fails.
func (d dockerRunner) sh(t *testing.T, command string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", d.id, "sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", command, err, out)
	}
	return string(out)
}

// realServer is the provider configured to reach the container.
func realServer(t *testing.T, d dockerRunner) integration.Server {
	t.Helper()
	s := newServer(t, (&capture{runner: d}).factory)
	if err := s.Configure(p.ConfigureRequest{Args: property.NewMap(homelabServerConfig())}); err != nil {
		t.Fatal(err)
	}
	return s
}
