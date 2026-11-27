package hosts

import (
	"reflect"
	"strings"
	"testing"
)

const real = "127.0.0.1\tlocalhost\n" +
	"::1\t\tlocalhost ip6-localhost ip6-loopback\n" +
	"ff02::1\t\tip6-allnodes\n" +
	"ff02::2\t\tip6-allrouters\n" +
	"127.0.1.1\toldname\n" +
	"10.0.0.5\tprinter.lan printer\n"

func name(t *testing.T, text, want string) {
	t.Helper()
	got, ok := HostsName(text)
	if !ok || got != want {
		t.Errorf("HostsName(%q) = %q, %v; want %q", text, got, ok, want)
	}
}

func TestHostsName(t *testing.T) {
	t.Run("finds the name on the 127.0.1.1 line", func(t *testing.T) {
		name(t, real, "oldname")
	})
	t.Run("takes the short name when the line is qualified", func(t *testing.T) {
		name(t, "127.0.1.1\thomelab.lan homelab\n", "homelab")
	})
	t.Run("says nothing when the file has no such line", func(t *testing.T) {
		if got, ok := HostsName("127.0.0.1\tlocalhost\n"); ok {
			t.Errorf("HostsName = %q, want none", got)
		}
	})
	t.Run("is not fooled by the localhost line above it", func(t *testing.T) {
		if got, _ := HostsName(real); got == "localhost" {
			t.Errorf("HostsName = localhost")
		}
	})
}

func TestSetHostsName(t *testing.T) {
	t.Run("replaces the name in place, leaving every other entry alone", func(t *testing.T) {
		updated := SetHostsName(real, "newname", "")
		name(t, updated, "newname")
		for _, kept := range []string{"10.0.0.5\tprinter.lan printer", "ff02::2"} {
			if !strings.Contains(updated, kept) {
				t.Errorf("lost %q: %q", kept, updated)
			}
		}
		if strings.Contains(updated, "oldname") {
			t.Errorf("kept oldname: %q", updated)
		}
	})
	t.Run("keeps the line in its original position", func(t *testing.T) {
		updated := strings.Split(SetHostsName(real, "newname", ""), "\n")
		if !strings.HasPrefix(updated[4], "127.0.1.1") {
			t.Errorf("line 4 = %q", updated[4])
		}
		if len(updated) != len(strings.Split(real, "\n")) {
			t.Errorf("length %d, want %d", len(updated), len(strings.Split(real, "\n")))
		}
	})
	t.Run("writes the qualified name before the short one, as Debian does", func(t *testing.T) {
		if got := SetHostsName(real, "homelab", "lan"); !strings.Contains(got, "127.0.1.1\thomelab.lan homelab") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("adds the line after 127.0.0.1 when the file has none", func(t *testing.T) {
		added := strings.Split(SetHostsName("127.0.0.1\tlocalhost\n10.0.0.5\tprinter\n", "homelab", ""), "\n")
		if added[1] != "127.0.1.1\thomelab" || added[2] != "10.0.0.5\tprinter" {
			t.Errorf("got %q", added)
		}
	})
	t.Run("gives the same file whether it runs once or twice", func(t *testing.T) {
		once := SetHostsName(real, "homelab", "lan")
		if twice := SetHostsName(once, "homelab", "lan"); twice != once {
			t.Errorf("twice %q != once %q", twice, once)
		}
	})
}

func TestHostsNames(t *testing.T) {
	names := func(t *testing.T, line string, want bool) {
		t.Helper()
		if got := HostsNamesHost(line, "homelab"); got != want {
			t.Errorf("HostsNamesHost(%q) = %v, want %v", line, got, want)
		}
	}
	t.Run("finds the short name when the qualified one comes first", func(t *testing.T) {
		name(t, "127.0.1.1\thomelab.lan homelab\n", "homelab")
	})
	t.Run("finds it when the qualified one comes second", func(t *testing.T) {
		name(t, "127.0.1.1\thomelab homelab.lan\n", "homelab")
	})
	t.Run("finds it when there is only one name", func(t *testing.T) {
		name(t, "127.0.1.1\thomelab\n", "homelab")
	})
	t.Run("lists every name on the line", func(t *testing.T) {
		want := []string{"homelab", "homelab.lan", "alias"}
		if got := HostsNames("127.0.1.1\thomelab homelab.lan alias\n"); !reflect.DeepEqual(got, want) {
			t.Errorf("HostsNames = %q, want %q", got, want)
		}
	})
	t.Run("answers whether the line names the host, in any position", func(t *testing.T) {
		for _, line := range []string{
			"127.0.1.1\thomelab\n",
			"127.0.1.1\thomelab.lan homelab\n",
			"127.0.1.1\thomelab homelab.lan\n",
			"127.0.1.1\tHomelab\n",
		} {
			names(t, line, true)
		}
	})
	t.Run("is case-insensitive, because hostnames are", func(t *testing.T) {
		names(t, "127.0.1.1\tHOMELAB\n", true)
	})
	t.Run("does not accept a line that names something else", func(t *testing.T) {
		names(t, "127.0.1.1\toldname\n", false)
		names(t, "127.0.0.1\thomelab\n", false)
	})
	t.Run("is not fooled by a name that merely starts the same way", func(t *testing.T) {
		names(t, "127.0.1.1\thomelab-old\n", false)
	})
}

var reply = strings.Join([]string{
	"homelab",
	"#pulumi-homelab#now",
	"homelab",
	"#pulumi-homelab#hosts",
	"127.0.0.1\tlocalhost",
	"127.0.1.1\thomelab",
	"",
}, "\n")

func TestParseHostnameOutput(t *testing.T) {
	t.Run("reads all three, and the third is not empty", func(t *testing.T) {
		found := ParseHostnameOutput(reply)
		if found.Static != "homelab" || found.Transient != "homelab" {
			t.Errorf("got %+v", found)
		}
		if !strings.Contains(found.HostsFile, "127.0.1.1\thomelab") {
			t.Errorf("hosts file %q", found.HostsFile)
		}
	})
	t.Run("finds the host on a tab-separated line, which is what Debian writes", func(t *testing.T) {
		if !HostsNamesHost(ParseHostnameOutput(reply).HostsFile, "homelab") {
			t.Error("host not found")
		}
	})
	t.Run("does not lose the hosts file to a marker appearing twice", func(t *testing.T) {
		if len(ParseHostnameOutput(reply).HostsFile) == 0 {
			t.Error("hosts file empty")
		}
	})
	t.Run("reads a static and transient name that differ, which DHCP can cause", func(t *testing.T) {
		found := ParseHostnameOutput(strings.Join([]string{"homelab", "#pulumi-homelab#now", "dhcp-given", "#pulumi-homelab#hosts", ""}, "\n"))
		if found.Static != "homelab" || found.Transient != "dhcp-given" {
			t.Errorf("got %+v", found)
		}
	})
	t.Run("answers empty rather than throwing when the machine said nothing", func(t *testing.T) {
		if got := ParseHostnameOutput(""); got != (HostnameOutput{}) {
			t.Errorf("got %+v", got)
		}
	})
}
