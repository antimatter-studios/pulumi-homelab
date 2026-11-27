package homelab

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
)

// Ported from src/resources/symlink.test.ts › the symlink resource. The TypeScript checked that the
// class exists; here the type is in the provider schema with its inputs.
func TestSchemaHasSymlink(t *testing.T) {
	t.Run("is constructible with a path and a target", func(t *testing.T) {
		server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.0.1"), integration.WithProvider(Provider()))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.GetSchema(p.GetSchemaRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Resources map[string]struct {
				RequiredInputs []string `json:"requiredInputs"`
			} `json:"resources"`
		}
		if err := json.Unmarshal([]byte(resp.Schema), &schema); err != nil {
			t.Fatal(err)
		}
		res, ok := schema.Resources["homelab:index:Symlink"]
		if !ok {
			t.Fatal("homelab:index:Symlink is not in the schema")
		}
		if strings.Join(res.RequiredInputs, ",") != "path,target" {
			t.Fatalf("required inputs %v, want path and target", res.RequiredInputs)
		}
	})
}

// Ported from src/resources/symlink.test.ts › deciding what is at the path.
func TestInterpretSymlink(t *testing.T) {
	t.Run("reads a link and its target", func(t *testing.T) {
		if got := interpretSymlink(0, "/mnt/data/journal"); got != (symlinkFound{State: "link", Target: "/mnt/data/journal"}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("reads nothing at all as absent, which is a resource to create", func(t *testing.T) {
		if got := interpretSymlink(9, ""); got != (symlinkFound{State: "absent"}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("reads something real as occupied, naming what it found", func(t *testing.T) {
		if got := interpretSymlink(8, "directory"); got != (symlinkFound{State: "occupied", Kind: "directory"}) {
			t.Fatalf("got %+v", got)
		}
		if got := interpretSymlink(8, "regular file"); got != (symlinkFound{State: "occupied", Kind: "regular file"}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("still calls it occupied when stat said nothing useful", func(t *testing.T) {
		if got := interpretSymlink(8, ""); got != (symlinkFound{State: "occupied", Kind: "something else"}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("reads a broken link as a link, because that is what it is", func(t *testing.T) {
		if got := interpretSymlink(0, "/mnt/data/never-mounted"); got != (symlinkFound{State: "link", Target: "/mnt/data/never-mounted"}) {
			t.Fatalf("got %+v", got)
		}
	})
}

// Ported from src/resources/symlink.test.ts › launcher scripts.
func TestLauncherTarget(t *testing.T) {
	t.Run("reads the program a launcher script execs", func(t *testing.T) {
		if got := launcherTarget("#!/bin/sh\nexec '/home/a/.t3/runtime/versions/0.1.13/t3' \"$@\"\n"); got != "/home/a/.t3/runtime/versions/0.1.13/t3" {
			t.Fatalf("got %q", got)
		}
		if got := launcherTarget("#!/bin/sh\nexec /opt/t3/t3 \"$@\"\n"); got != "/opt/t3/t3" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("refuses anything that is not a small exec script", func(t *testing.T) {
		for _, text := range []string{"ELF binary", "#!/bin/sh\necho hello\n", "#!/bin/sh\nexec /x\n" + strings.Repeat("#", 5000)} {
			if got := launcherTarget(text); got != "" {
				t.Errorf("launcherTarget(%.40q) = %q, want none", text, got)
			}
		}
	})
	// JavaScript semantics the regular expression carries: the m flag's ^ after \r, \s that
	// includes a no-break space, and a double-quoted program.
	t.Run("matches as the JavaScript expression does", func(t *testing.T) {
		for text, want := range map[string]string{
			"#!/bin/sh\rexec /cr\n":                  "/cr",
			"#!/bin/sh\n exec /nbsp\n":     "/nbsp",
			"#!/bin/sh\nexec \"/opt/a b/t3\" \"$@\"": "/opt/a b/t3",
			"#!/bin/sh\nexec /x more":           "/x",
			"#!/bin/sh\n  # exec /not\n":             "",
		} {
			if got := launcherTarget(text); got != want {
				t.Errorf("launcherTarget(%q) = %q, want %q", text, got, want)
			}
		}
	})
}

func TestSymlinkParent(t *testing.T) {
	for path, want := range map[string]string{"/var/log/journal": "/var/log", "/journal": "/", "journal": "journal", "/a/b/": "/a/b"} {
		if got := symlinkParent(path); got != want {
			t.Errorf("symlinkParent(%q) = %q, want %q", path, got, want)
		}
	}
}
