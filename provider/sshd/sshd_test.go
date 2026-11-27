package sshd

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

const real = `passwordauthentication no
pubkeyauthentication yes
permitrootlogin without-password
maxauthtries 6
maxstartups 10:30:100
gatewayports no`

func TestParseSshdT(t *testing.T) {
	t.Run("reads the keywords sshd prints, which are lowercased", func(t *testing.T) {
		if got := ParseSshdT(real)["passwordauthentication"]; got != "no" {
			t.Errorf("passwordauthentication = %q", got)
		}
		if got := ParseSshdT(real)["maxauthtries"]; got != "6" {
			t.Errorf("maxauthtries = %q", got)
		}
	})
	t.Run("turns without-password into the spelling anybody would write", func(t *testing.T) {
		if got := ParseSshdT(real)["permitrootlogin"]; got != "prohibit-password" {
			t.Errorf("permitrootlogin = %q", got)
		}
		if got := NormaliseSshdValue("without-password"); got != "prohibit-password" {
			t.Errorf("NormaliseSshdValue = %q", got)
		}
	})
	t.Run("leaves every other value exactly as sshd said it", func(t *testing.T) {
		if got := NormaliseSshdValue("prohibit-password"); got != "prohibit-password" {
			t.Errorf("NormaliseSshdValue = %q", got)
		}
		if got := ParseSshdT(real)["maxstartups"]; got != "10:30:100" {
			t.Errorf("maxstartups = %q", got)
		}
	})
	t.Run("keeps a value containing spaces whole", func(t *testing.T) {
		if got := ParseSshdT("subsystem sftp /usr/lib/openssh/sftp-server")["subsystem"]; got != "sftp /usr/lib/openssh/sftp-server" {
			t.Errorf("subsystem = %q", got)
		}
	})
	t.Run("says nothing when sshd could not answer", func(t *testing.T) {
		if got := ParseSshdT(""); len(got) != 0 {
			t.Errorf("ParseSshdT(\"\") = %v", got)
		}
	})
}

func TestRenderSshd(t *testing.T) {
	keyword := func(t *testing.T, in, want string) {
		t.Helper()
		if got := KeywordFor(in); got != want {
			t.Errorf("KeywordFor(%q) = %q, want %q", in, got, want)
		}
	}
	render := func(t *testing.T, settings []Setting, unchecked []Keyword, want []Keyword) {
		t.Helper()
		if got := RenderSshd(settings, unchecked); !reflect.DeepEqual(got, want) {
			t.Errorf("RenderSshd = %v, want %v", got, want)
		}
	}
	t.Run("capitalises a property into the keyword sshd uses", func(t *testing.T) {
		keyword(t, "passwordAuthentication", "PasswordAuthentication")
		keyword(t, "maxAuthTries", "MaxAuthTries")
	})
	t.Run("knows the ones that are not simply capitalised", func(t *testing.T) {
		keyword(t, "usePAM", "UsePAM")
		keyword(t, "useDNS", "UseDNS")
		keyword(t, "x11Forwarding", "X11Forwarding")
		keyword(t, "macs", "MACs")
	})
	t.Run("writes booleans as yes and no, which sshd requires", func(t *testing.T) {
		render(t, []Setting{{"passwordAuthentication", false}, {"pubkeyAuthentication", true}}, nil,
			[]Keyword{{"PasswordAuthentication", "no"}, {"PubkeyAuthentication", "yes"}})
	})
	t.Run("writes lists comma-separated", func(t *testing.T) {
		render(t, []Setting{{"allowUsers", []string{"admin", "deploy"}}}, nil, []Keyword{{"AllowUsers", "admin,deploy"}})
	})
	t.Run("writes numbers as themselves", func(t *testing.T) {
		render(t, []Setting{{"maxAuthTries", 3}, {"port", 22}}, nil, []Keyword{{"MaxAuthTries", "3"}, {"Port", "22"}})
	})
	t.Run("leaves out what was never set", func(t *testing.T) {
		render(t, nil, nil, nil)
	})
	t.Run("lets unchecked reach a keyword the type does not name", func(t *testing.T) {
		render(t, nil, []Keyword{{"RekeyLimit", "1G 1h"}}, []Keyword{{"RekeyLimit", "1G 1h"}})
	})
}

func TestSshdDropIn(t *testing.T) {
	t.Run("writes a plain file when there is no match", func(t *testing.T) {
		file := SshdDropIn([]Keyword{{"PasswordAuthentication", "no"}}, nil)
		if !strings.Contains(file, "PasswordAuthentication no") {
			t.Errorf("missing setting: %q", file)
		}
		if strings.Contains(file, "Match") {
			t.Errorf("unexpected Match: %q", file)
		}
	})
	t.Run("puts the settings inside the block, indented", func(t *testing.T) {
		file := SshdDropIn([]Keyword{{"PasswordAuthentication", "yes"}}, []Criterion{{"Address", "10.0.0.0/24"}})
		if !strings.Contains(file, "Match Address 10.0.0.0/24") {
			t.Errorf("missing Match: %q", file)
		}
		if !strings.Contains(file, "    PasswordAuthentication yes") {
			t.Errorf("missing indented setting: %q", file)
		}
	})
	t.Run("joins several criteria into one Match line", func(t *testing.T) {
		file := SshdDropIn([]Keyword{{"X11Forwarding", "no"}}, []Criterion{{"User", "deploy"}, {"Address", "10.0.0.0/24"}})
		if !strings.Contains(file, "Match User deploy Address 10.0.0.0/24") {
			t.Errorf("missing Match: %q", file)
		}
	})
	t.Run("says who wrote it, for whoever finds it", func(t *testing.T) {
		if file := SshdDropIn(nil, nil); !strings.HasPrefix(file, "# Managed by Pulumi.") {
			t.Errorf("no header: %q", file)
		}
	})
}

func TestHasInclude(t *testing.T) {
	check := func(t *testing.T, text string, want bool) {
		t.Helper()
		if got := HasInclude(text, Directory); got != want {
			t.Errorf("HasInclude(%q) = %v, want %v", text, got, want)
		}
	}
	t.Run("finds the Include that makes a drop-in mean anything", func(t *testing.T) {
		check(t, "Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n", true)
	})
	t.Run("is not case-sensitive about the keyword, since sshd is not", func(t *testing.T) {
		check(t, "include /etc/ssh/sshd_config.d/*.conf\n", true)
	})
	t.Run("reports its absence, which is the silent failure this guards", func(t *testing.T) {
		check(t, "Port 22\nPermitRootLogin no\n", false)
	})
	t.Run("does not accept an Include of somewhere else", func(t *testing.T) {
		check(t, "Include /etc/ssh/other.d/*.conf\n", false)
	})
}

func TestRenderSshdWritesValuesAsJavaScriptsString(t *testing.T) {
	got := RenderSshd([]Setting{
		{"maxStartups", float64(1000000)},
		{"clientAliveInterval", 15},
		{"loginGraceTime", math.Copysign(0, -1)},
		{"maxAuthTries", 1.5},
		{"allowUsers", []any{"admin", "deploy"}},
		{"allowGroups", []any{"a", nil, 3.0, true}},
		{"denyUsers", []string{"x", "y"}},
	}, nil)
	want := []Keyword{
		{"MaxStartups", "1000000"},
		{"ClientAliveInterval", "15"},
		{"LoginGraceTime", "0"},
		{"MaxAuthTries", "1.5"},
		{"AllowUsers", "admin,deploy"},
		{"AllowGroups", "a,,3,true"},
		{"DenyUsers", "x,y"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestKeywordForCapitalisesTheFirstCharacterAsJavaScriptDoes(t *testing.T) {
	for in, want := range map[string]string{
		"éx":          "Éx",
		"ßx":          "SSx",
		"\U00010428x": "\U00010428x", // charAt(0) is half a surrogate pair, which toUpperCase leaves
		"":            "",
		"x":           "X",
	} {
		if got := KeywordFor(in); got != want {
			t.Errorf("KeywordFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSshdTTrimsWhatJavaScriptTrims(t *testing.T) {
	got := ParseSshdT("\ufeffport 22\u3000\nx11forwarding no\u0085\n")
	want := map[string]string{"port": "22", "x11forwarding": "no\u0085"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if !HasInclude("\ufeffInclude\u00a0/etc/ssh/sshd_config.d/*.conf", Directory) {
		t.Error("an Include split by NBSP after a BOM was missed")
	}
	if HasInclude("Include\u0085/etc/ssh/sshd_config.d/*.conf", Directory) {
		t.Error("NEL is not whitespace in JavaScript, so this line is one word")
	}
}

// Settings from a resource's inputs render in the order the TypeScript actually wrote them: typed
// properties sorted, then unchecked keywords sorted. antimatter-vps's homelab-tunnel-sshd declares
// AllowTcpForwarding, GatewayPorts, X11Forwarding, AllowAgentForwarding, ClientAliveInterval,
// ClientAliveCountMax and an unchecked PermitTTY; its drop-in lists them as below.
func TestRenderSshdInputUsesTheProductionOrder(t *testing.T) {
	got := RenderSshdInput(map[string]any{
		"allowTcpForwarding":   "remote",
		"gatewayPorts":         "clientspecified",
		"x11Forwarding":        false,
		"allowAgentForwarding": false,
		"clientAliveInterval":  30.0,
		"clientAliveCountMax":  3.0,
	}, map[string]string{"PermitTTY": "no"})
	var order []string
	for _, k := range got {
		order = append(order, k.Keyword)
	}
	want := []string{"AllowAgentForwarding", "AllowTcpForwarding", "ClientAliveCountMax", "ClientAliveInterval", "GatewayPorts", "X11Forwarding", "PermitTTY"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order %v, want %v", order, want)
	}
	// an unchecked keyword repeating a typed one keeps the typed one's place
	over := RenderSshdInput(map[string]any{"gatewayPorts": "no", "allowTcpForwarding": "no"}, map[string]string{"GatewayPorts": "yes", "Banner": "none"})
	if fmt := SshdDropIn(over, MatchFromInput(map[string]string{"User": "tunnel", "Address": "10.0.0.0/8"})); fmt !=
		"# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\nMatch Address 10.0.0.0/8 User tunnel\n    AllowTcpForwarding no\n    GatewayPorts yes\n    Banner none\n" {
		t.Fatalf("got %q", fmt)
	}
}
