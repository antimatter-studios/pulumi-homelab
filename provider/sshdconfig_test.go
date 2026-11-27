package homelab

import (
	"strings"
	"testing"
)

func fp(v float64) *float64 { return &v }
func bp(v bool) *bool       { return &v }

// #45: the drop-ins on machines list typed settings sorted by property name, then unchecked
// keywords sorted among themselves, then Match criteria sorted by key. antimatter-vps
// homelab-tunnel-sshd, declared in another order, must render exactly so.
func TestSshdConfigRendersTheProductionOrder(t *testing.T) {
	args := SshdConfigArgs{
		Settings: &SshdSettings{
			AllowTcpForwarding: strp("remote"), GatewayPorts: strp("clientspecified"), X11Forwarding: bp(false),
			AllowAgentForwarding: bp(false), ClientAliveInterval: fp(30), ClientAliveCountMax: fp(3),
		},
		Unchecked: map[string]string{"PermitTTY": "no"},
		Match:     &SshdMatch{User: strp("tunnel"), Address: strp("10.0.0.0/8")},
	}
	keywords, settings, criteria, match := sshdRendered(args)
	var order []string
	for _, k := range keywords {
		order = append(order, k.Keyword)
	}
	want := "AllowAgentForwarding,AllowTcpForwarding,ClientAliveCountMax,ClientAliveInterval,GatewayPorts,X11Forwarding,PermitTTY"
	if strings.Join(order, ",") != want {
		t.Errorf("order %v", order)
	}
	if settings["ClientAliveInterval"] != "30" || settings["X11Forwarding"] != "no" || match["User"] != "tunnel" {
		t.Errorf("%v %v", settings, match)
	}
	if criteria[0].Key != "Address" || criteria[1].Key != "User" {
		t.Errorf("criteria %v", criteria)
	}
}

func TestSshdConfigWritesNumbersAsJavaScriptDoes(t *testing.T) {
	_, settings, _, _ := sshdRendered(SshdConfigArgs{Settings: &SshdSettings{LoginGraceTime: fp(1000000), Ciphers: []string{"a", "b"}}})
	if settings["LoginGraceTime"] != "1000000" || settings["Ciphers"] != "a,b" {
		t.Errorf("%v", settings)
	}
}

// Review of #98: an import recovers typed settings, so re-rendering keeps the original keyword
// order byte for byte, and a drop-in that would be rewritten is refused.
func TestSshdArgsFromDropInKeepsTheOriginalOrder(t *testing.T) {
	tunnel := SshdConfigArgs{
		Settings: &SshdSettings{AllowTcpForwarding: strp("remote"), GatewayPorts: strp("clientspecified"), X11Forwarding: bp(false),
			AllowAgentForwarding: bp(false), ClientAliveInterval: fp(15), ClientAliveCountMax: fp(3)},
		Unchecked: map[string]string{"PermitTTY": "no"},
		Match:     &SshdMatch{User: strp("tunnel")},
	}
	keywords, _, criteria, _ := sshdRendered(tunnel)
	content := sshdDropInOf(keywords, criteria)
	args, ok := sshdArgsFromDropIn(content)
	if !ok {
		t.Fatalf("refused a drop-in it wrote:\n%s", content)
	}
	again, _, againCriteria, _ := sshdRendered(args)
	if sshdDropInOf(again, againCriteria) != content || *args.Settings.ClientAliveInterval != 15 || args.Unchecked["PermitTTY"] != "no" || len(args.Unchecked) != 1 {
		t.Errorf("%+v %v", *args.Settings, args.Unchecked)
	}
	head := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n"
	for _, bad := range []string{
		"PasswordAuthentication no\n",                    // not written by this resource
		head + "X11Forwarding no\nZzz x\nAaa y\n",        // unchecked keywords out of order: no split renders it
		head + "PermitTTY no\nAllowAgentForwarding no\n", // unchecked before typed
		head + "Match Nonsense x\n    PermitTTY no\n",    // a criterion this resource does not write
	} {
		if _, ok := sshdArgsFromDropIn(bad); ok {
			t.Errorf("imported %q", bad)
		}
	}
}

func TestParseSshdRead(t *testing.T) {
	out := "Include /etc/ssh/sshd_config.d/*.conf\n#pulumi-homelab#drop\n#pulumi-homelab#effective\npasswordauthentication no\npermitrootlogin without-password\n"
	got := parseSshdRead(out, sshdDirectory)
	if !got.included || got.content != nil || got.effective["permitrootlogin"] != "prohibit-password" {
		t.Errorf("%+v", got)
	}
	if over := sshdOverridden(map[string]string{"PasswordAuthentication": "yes", "Nonsense": "x"}, got.effective); strings.Join(over, ",") != "PasswordAuthentication" {
		t.Errorf("%v", over)
	}
}

// Review of #98: typed X11Forwarding followed by unchecked AllowAgentForwarding is a drop-in the
// renderer writes, so the import finds that split instead of promoting both and reordering them.
func TestSshdArgsFromDropInFindsTheTypedUncheckedBoundary(t *testing.T) {
	head := "# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.\n"
	content := head + "X11Forwarding no\nAllowAgentForwarding no\n"
	args, ok := sshdArgsFromDropIn(content)
	if !ok || args.Settings.X11Forwarding == nil || args.Settings.AllowAgentForwarding != nil || args.Unchecked["AllowAgentForwarding"] != "no" {
		t.Fatalf("%v %+v %v", ok, args.Settings, args.Unchecked)
	}
	keywords, _, criteria, _ := sshdRendered(args)
	if sshdDropInOf(keywords, criteria) != content {
		t.Fatal("does not render back")
	}
}
