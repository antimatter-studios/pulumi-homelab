package homelab

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Ported from src/resources/packages.test.ts: working out what still needs installing.
func TestMissingFrom(t *testing.T) {
	t.Run("names the ones dpkg does not have, in the order they were declared", func(t *testing.T) {
		if got := missingFrom([]string{"btop", "gdu", "duf"}, map[string]string{"btop": "1.2.13-1"}); !reflect.DeepEqual(got, []string{"gdu", "duf"}) {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("counts a multi-arch package as present under its bare name", func(t *testing.T) {
		if got := missingFrom([]string{"libc6"}, map[string]string{"libc6:arm64": "2.36-9"}); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("has nothing to do on a machine that already matches", func(t *testing.T) {
		if got := missingFrom([]string{"btop"}, map[string]string{"btop": "1.2.13-1"}); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("asks for everything on a machine that has none of it", func(t *testing.T) {
		if got := missingFrom([]string{"btop", "gdu"}, map[string]string{}); !reflect.DeepEqual(got, []string{"btop", "gdu"}) {
			t.Fatalf("got %v", got)
		}
	})
}

// Ported from src/resources/packages.test.ts: composing what brings the present half into line.
func TestPresentCommands(t *testing.T) {
	t.Run("does nothing when the machine already matches", func(t *testing.T) {
		if got := presentCommands(nil, nil, false); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("installs what is missing, in one apt-get", func(t *testing.T) {
		commands := presentCommands([]string{"ovmf", "nginx"}, nil, false)
		if len(commands) != 1 || !strings.Contains(commands[0], "'ovmf' 'nginx'") {
			t.Fatalf("got %v", commands)
		}
	})
	t.Run("marks what apt holds as auto, even with nothing to install", func(t *testing.T) {
		commands := presentCommands(nil, []string{"ovmf"}, false)
		if len(commands) != 1 || !strings.Contains(commands[0], "apt-mark manual") {
			t.Fatalf("got %v", commands)
		}
	})
	t.Run("installs before it marks", func(t *testing.T) {
		commands := presentCommands([]string{"nginx"}, []string{"ovmf"}, false)
		install := slices.IndexFunc(commands, func(c string) bool { return strings.Contains(c, aptGet+" install") })
		mark := slices.IndexFunc(commands, func(c string) bool { return strings.Contains(c, "apt-mark") })
		if install < 0 || mark < 0 || install >= mark {
			t.Fatalf("got %v", commands)
		}
	})
	t.Run("waits for the dpkg lock rather than failing on it", func(t *testing.T) {
		for _, c := range presentCommands([]string{"nginx"}, nil, true) {
			if !strings.Contains(c, "-o DPkg::Lock::Timeout=") {
				t.Fatalf("%q does not wait for the lock", c)
			}
		}
	})
	t.Run("refreshes the lists first when asked", func(t *testing.T) {
		if got := presentCommands([]string{"nginx"}, nil, true)[0]; got != aptGet+" update -qq" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("refreshes nothing when not asked", func(t *testing.T) {
		if got := strings.Join(presentCommands([]string{"nginx"}, nil, false), " "); strings.Contains(got, "apt-get update") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("never prompts", func(t *testing.T) {
		if got := presentCommands([]string{"nginx"}, nil, false)[0]; !strings.Contains(got, "DEBIAN_FRONTEND=noninteractive") {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("never removes or autoremoves anything", func(t *testing.T) {
		commands := strings.Join(presentCommands([]string{"nginx"}, []string{"ovmf"}, true), " ")
		for _, word := range []string{"autoremove", "purge", "apt-mark auto"} {
			if strings.Contains(commands, word) {
				t.Fatalf("%q contains %q", commands, word)
			}
		}
	})
	t.Run("is the same list every time, so a refresh does not look like a change", func(t *testing.T) {
		if a, b := presentCommands([]string{"nginx"}, []string{"ovmf"}, true), presentCommands([]string{"nginx"}, []string{"ovmf"}, true); !reflect.DeepEqual(a, b) {
			t.Fatalf("%v != %v", a, b)
		}
	})
}

func boolp(v bool) *bool { return &v }

// Ported from src/resources/packages.test.ts: deciding whether the list has stopped being true.
func TestPackagesChanged(t *testing.T) {
	next := packagesWanted{Present: []string{"ovmf"}, Absent: []string{"telnet"}, Update: false}
	matching := func() packagesRecord {
		return packagesRecord{Present: []string{"ovmf"}, Absent: []string{"telnet"}, Update: boolp(false),
			Missing: []string{}, Lingering: []string{}, Auto: []string{}}
	}
	with := func(change func(*packagesRecord)) packagesRecord {
		r := matching()
		change(&r)
		return r
	}
	t.Run("is quiet when the machine matches the declaration", func(t *testing.T) {
		if packagesChanged(matching(), next) {
			t.Fatal("changed")
		}
	})
	t.Run("reports a declared package apt holds as auto", func(t *testing.T) {
		if !packagesChanged(with(func(r *packagesRecord) { r.Auto = []string{"ovmf"} }), next) {
			t.Fatal("not changed")
		}
	})
	t.Run("reports a package that is missing, and one that is back", func(t *testing.T) {
		if !packagesChanged(with(func(r *packagesRecord) { r.Missing = []string{"ovmf"} }), next) {
			t.Fatal("missing not changed")
		}
		if !packagesChanged(with(func(r *packagesRecord) { r.Lingering = []string{"telnet"} }), next) {
			t.Fatal("lingering not changed")
		}
	})
	t.Run("reports a change to either list", func(t *testing.T) {
		if !packagesChanged(matching(), packagesWanted{Present: []string{"ovmf", "nginx"}, Absent: next.Absent}) {
			t.Fatal("present")
		}
		if !packagesChanged(matching(), packagesWanted{Present: next.Present, Absent: []string{}}) {
			t.Fatal("absent")
		}
		if !packagesChanged(matching(), packagesWanted{Present: next.Present, Absent: next.Absent, Update: true}) {
			t.Fatal("update")
		}
	})
	t.Run("reads the older spelling of the present list", func(t *testing.T) {
		if packagesChanged(with(func(r *packagesRecord) { r.Present, r.Names = nil, []string{"ovmf"} }), next) {
			t.Fatal("changed")
		}
	})
	t.Run("reports a change when the state never recorded whether lists are refreshed", func(t *testing.T) {
		if !packagesChanged(with(func(r *packagesRecord) { r.Update = nil }), next) {
			t.Fatal("not changed")
		}
		if packagesChanged(matching(), next) {
			t.Fatal("changed")
		}
	})
	t.Run("treats a state with nothing recorded as changed rather than as matching", func(t *testing.T) {
		if !packagesChanged(packagesRecord{}, next) {
			t.Fatal("not changed")
		}
	})
}

// Ported from src/resources/packages.test.ts: unattended installs.
func TestPresentCommandsUnattended(t *testing.T) {
	t.Run("keeps a configuration file that is already there instead of asking about it", func(t *testing.T) {
		if got := presentCommands([]string{"containerd.io"}, nil, false)[0]; !strings.Contains(got, "--force-confold") {
			t.Fatalf("got %q", got)
		}
	})
}

// The exact apt-get options, which are the substance of src/resources/aptget.ts.
func TestAptGetOptions(t *testing.T) {
	want := "apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
	if aptGet != want {
		t.Fatalf("got %q", aptGet)
	}
	if got := purgeCommand("it's"); got != "DEBIAN_FRONTEND=noninteractive "+want+` purge -y -qq 'it'\''s'` {
		t.Fatalf("got %q", got)
	}
}

// The contradiction check and the purge guard, with their exact texts.
func TestPackagesRefusals(t *testing.T) {
	_, err := wantedPackages(AptPackagesArgs{Present: []string{"a", "b"}, Absent: []string{"b"}})
	if err == nil || err.Error() != "declared both present and absent, which is not a machine that can exist: b" {
		t.Fatalf("got %v", err)
	}
	if got, _ := wantedPackages(AptPackagesArgs{Names: []string{"a"}}); !reflect.DeepEqual(got.Present, []string{"a"}) {
		t.Fatalf("names is not read as present: %v", got)
	}
	want := "refusing to purge player: apt would also remove player-data, libc6. " +
		"Declaring a package absent is a statement about that package, not permission to remove what depends on it."
	if got := purgeRefusal([]string{"player"}, []string{"player", "player-data", "libc6:arm64"}); got != want {
		t.Fatalf("got %q", got)
	}
	if got := purgeRefusal([]string{"player"}, []string{"player:arm64"}); got != "" {
		t.Fatalf("got %q", got)
	}
}
