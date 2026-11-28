package homelab

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func equal[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func deepEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func contains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%q does not contain %q", got, want)
	}
}

func matches(t *testing.T, got, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(got) {
		t.Errorf("%q does not match %q", got, pattern)
	}
}

func TestFluxChecks(t *testing.T) {
	t.Run("waits for Ready", func(t *testing.T) {
		contains(t, FluxReady("app", nil), "wait")
		contains(t, FluxReady("app", nil), "--for=condition=Ready")
	})
	t.Run("defaults to a Kustomization in flux-system with the k3s kubeconfig", func(t *testing.T) {
		equal(t, FluxReady("app", nil), "KUBECONFIG='/etc/rancher/k3s/k3s.yaml' kubectl -n 'flux-system' wait 'kustomization/app' --for=condition=Ready --timeout='120s' >/dev/null 2>&1")
	})
	t.Run("can check a GitRepository", func(t *testing.T) {
		contains(t, FluxReady("app", &FluxCheckArgs{Kind: new("gitrepository")}), "'gitrepository/app'")
	})
	t.Run("takes a timeout", func(t *testing.T) {
		contains(t, FluxReady("app", &FluxCheckArgs{Timeout: new("300s")}), "--timeout='300s'")
	})
	t.Run("quotes every shell argument", func(t *testing.T) {
		equal(t, FluxReady("ai'world", &FluxCheckArgs{
			Kubeconfig: new("/a'b"), Namespace: new("n's"), Kind: new("k'ind"), Timeout: new("3'00s"),
		}), `KUBECONFIG='/a'\''b' kubectl -n 'n'\''s' wait 'k'\''ind/ai'\''world' --for=condition=Ready --timeout='3'\''00s' >/dev/null 2>&1`)
	})
	t.Run("reads the Ready condition message", func(t *testing.T) {
		contains(t, FluxReason("app", nil), `.status.conditions[?(@.type=="Ready")].message`)
	})
	t.Run("provides a command with sudo", func(t *testing.T) {
		equal(t, FluxReason("app", nil), `sudo KUBECONFIG=/etc/rancher/k3s/k3s.yaml kubectl -n flux-system get kustomization app -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'`)
	})
}

func TestIgnoreRules(t *testing.T) {
	t.Run("ignores everything then re-includes requested paths", func(t *testing.T) {
		equal(t, IgnoreRules([]string{"/chart", "/deploy"}), "/*\n!/chart\n!/deploy")
	})
	t.Run("supplies a leading slash", func(t *testing.T) {
		equal(t, IgnoreRules([]string{"chart"}), "/*\n!/chart")
	})
}

func TestClaudeSharedTarget(t *testing.T) {
	for _, tc := range []struct{ label, backend, entry, want string }{
		{"joins the backend and entry", "/home/agent/.claude", "projects", "/home/agent/.claude/projects"},
		{"strips a trailing slash", "/home/agent/.claude/", "sessions", "/home/agent/.claude/sessions"},
		{"strips several trailing slashes", "/home/agent/.claude///", "plugins", "/home/agent/.claude/plugins"},
		{"keeps the absolute target", "/mnt/storage/t3code/.claude", "settings.json", "/mnt/storage/t3code/.claude/settings.json"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			equal(t, SharedTarget(tc.backend, tc.entry), tc.want)
		})
	}
}

func TestClaudeProfileRefusal(t *testing.T) {
	const backend = "/home/agent/.claude"
	for _, tc := range []struct {
		label, dir string
		refused    bool
	}{
		{"accepts a sibling profile", backend + "-work", false},
		{"refuses the backend", backend, true},
		{"refuses the backend with a trailing slash", backend + "/", true},
		{"accepts a nested profile", backend + "/other", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			err := ProfileRefusal(backend, tc.dir)
			equal(t, err != nil, tc.refused)
			if err != nil {
				contains(t, err.Error(), "dir and shared are the same directory")
			}
		})
	}
}

func TestClaudeSharedDefaults(t *testing.T) {
	names := []string{}
	entries := map[string]SharedEntry{}
	for _, entry := range SharedDefaults {
		names = append(names, entry.Name)
		entries[entry.Name] = entry
	}
	t.Run("shares the working entries", func(t *testing.T) {
		deepEqual(t, names, []string{"projects", "sessions", "session-env", "plugins", "settings.json"})
	})
	t.Run("leaves credentials private", func(t *testing.T) {
		for _, name := range []string{".credentials.json", ".claude.json", "history.jsonl"} {
			_, found := entries[name]
			equal(t, found, false)
		}
	})
	t.Run("uses private modes for conversations and group modes for other directories", func(t *testing.T) {
		for name, mode := range map[string]string{"projects": "0700", "sessions": "0700", "session-env": "0775", "plugins": "0775"} {
			equal(t, stringOr(entries[name].Mode, ""), mode)
		}
	})
	t.Run("declares settings.json as a file", func(t *testing.T) {
		equal(t, entries["settings.json"].Kind, "file")
		equal(t, entries["settings.json"].Mode, nil)
	})
}

func TestT3Default(t *testing.T) {
	t.Run("pins the release and checksum together", func(t *testing.T) {
		matches(t, T3Default.Version, `^\d+\.\d+\.\d+-nightly\.\d+\.\d+$`)
		matches(t, T3Default.SHA256, `^[0-9a-f]{64}$`)
	})
	t.Run("builds the URL from the version", func(t *testing.T) {
		equal(t, T3Default.URL("1.2.3-nightly.4.5"), "https://github.com/pingdotgg/t3code/releases/download/v1.2.3-nightly.4.5/t3-1.2.3-nightly.4.5-linux-x64.tar.gz")
	})
	t.Run("uses a nightly release", func(t *testing.T) {
		contains(t, T3Default.Version, "nightly")
	})
}

func TestAgentDefaults(t *testing.T) {
	names, bins := []string{}, []string{}
	archives := []ArchiveAgentSpec{}
	official := []OfficialAgentSpec{}
	for _, agent := range AgentDefaults {
		switch agent := agent.(type) {
		case ArchiveAgentSpec:
			names = append(names, agent.Key)
			bins = append(bins, agent.Bin)
			archives = append(archives, agent)
		case OfficialAgentSpec:
			names = append(names, agent.Key)
			bins = append(bins, agent.Bin)
			official = append(official, agent)
		default:
			t.Fatalf("unexpected agent type %T", agent)
		}
	}
	t.Run("installs gh and the three agents", func(t *testing.T) {
		deepEqual(t, names, []string{"gh", "claude", "codex", "opencode"})
	})
	t.Run("pins gh by checksum", func(t *testing.T) {
		equal(t, len(archives), 1)
		for _, agent := range archives {
			equal(t, agent.Key, "gh")
			matches(t, agent.SHA256, `^[0-9a-f]{64}$`)
		}
	})
	t.Run("uses official installers for the three agents", func(t *testing.T) {
		keys := []string{}
		for _, agent := range official {
			keys = append(keys, agent.Key)
			matches(t, agent.Installer, `^curl -fsSL https://`)
			equal(t, strings.HasPrefix(agent.Bin, "/"), false)
			if agent.Key == "codex" {
				contains(t, agent.Installer, "CODEX_NON_INTERACTIVE=1")
			}
		}
		deepEqual(t, keys, []string{"claude", "codex", "opencode"})
	})
	t.Run("names each executable by its relative path", func(t *testing.T) {
		for _, bin := range bins {
			equal(t, bin != "" && !strings.HasPrefix(bin, "/"), true)
		}
	})
}

func TestUnitResourceLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		args *UnitArgs
		want string
	}{
		{"leaves existing units unchanged by default", nil, ""},
		{"writes each limit and TMPDIR as a service line", &UnitArgs{
			Limits: UnitLimits{MemoryHigh: "6G", MemoryMax: "8G", CPUWeight: new(20), IOWeight: new(20)},
			TmpDir: "/mnt/storage/t3code/tmp",
		}, "MemoryHigh=6G\nMemoryMax=8G\nCPUWeight=20\nIOWeight=20\nEnvironment=TMPDIR=/mnt/storage/t3code/tmp\n"},
		{"caps swap and sets the OOM policy", &UnitArgs{Limits: UnitLimits{
			MemoryMax: "8G", MemorySwapMax: "32G", OOMPolicy: "continue",
		}}, "MemoryMax=8G\nMemorySwapMax=32G\nOOMPolicy=continue\n"},
		{"omits swap and OOM policy when unset", &UnitArgs{Limits: UnitLimits{MemoryMax: "8G"}}, "MemoryMax=8G\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := UnitResourceLines(tc.args)
			equal(t, err, nil)
			want := tc.want
			if want != "" {
				want = "\n# What the agents may take of the machine, and where their scratch goes. Declared by the stack.\n" + want
			}
			equal(t, got, want)
			if tc.args == nil {
				got, err = UnitResourceLines(&UnitArgs{Name: new("t3code")})
				equal(t, err, nil)
				equal(t, got, "")
			}
		})
	}
	t.Run("refuses weights outside the systemd range", func(t *testing.T) {
		// Weight fields use ints, so Go rejects fractional values at compile time.
		for _, tc := range []struct {
			name   string
			limits UnitLimits
		}{
			{"CPUWeight", UnitLimits{CPUWeight: new(0)}},
			{"IOWeight", UnitLimits{IOWeight: new(20000)}},
		} {
			_, err := UnitResourceLines(&UnitArgs{Limits: tc.limits})
			if err == nil {
				t.Errorf("accepted invalid %s", tc.name)
				continue
			}
			contains(t, err.Error(), fmt.Sprintf("%s must be an integer from 1 to 10000", tc.name))
		}
	})
}
