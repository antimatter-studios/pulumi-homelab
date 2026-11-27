package samba

import (
	"reflect"
	"strings"
	"testing"
)

const testparm = `[global]
	log file = /var/log/samba/log.%m
	server role = STANDALONE SERVER

[public]
	guest ok = Yes
	path = /mnt/data

[admin]
	path = /mnt/data
	read only = No
	valid users = admin
`

const smbConf = `# Sambaconfig, hand-edited 2024
[global]
   workgroup = WORKGROUP
   # player needs this
   server min protocol = SMB2

[public]
   path = /mnt/data
   guest ok = yes

[admin]
   path = /mnt/data
   read only = no
   valid users = admin
`

func has(text, section string) bool {
	_, ok := ParseSections(text)[section]
	return ok
}

func contains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("%q does not contain %q", text, want)
		}
	}
}

func lacks(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(text, u) {
			t.Errorf("%q contains %q", text, u)
		}
	}
}

func TestEffectiveShare(t *testing.T) {
	t.Run("finds a share and its settings", func(t *testing.T) {
		got, ok := EffectiveShare(testparm, "admin")
		want := map[string]string{"path": "/mnt/data", "read only": "No", "valid users": "admin"}
		if !ok || !reflect.DeepEqual(got.Map(), want) {
			t.Errorf("EffectiveShare = %v, %v", got, ok)
		}
	})
	t.Run("keeps keys that have spaces in them, which most samba keys do", func(t *testing.T) {
		got, _ := EffectiveShare(testparm, "public")
		if v, _ := got.Get("guest ok"); v != "Yes" {
			t.Errorf("guest ok = %q", v)
		}
	})
	t.Run("says nothing rather than something wrong about a share that is not there", func(t *testing.T) {
		if got, ok := EffectiveShare(testparm, "media"); ok {
			t.Errorf("EffectiveShare = %v", got)
		}
	})
	t.Run("reads [global] as a section like any other", func(t *testing.T) {
		if !has(testparm, "global") {
			t.Error("no global")
		}
	})
}

func TestUpsertSection(t *testing.T) {
	t.Run("replaces a section in place, leaving the rest byte for byte", func(t *testing.T) {
		updated := UpsertSection(smbConf, "public", ShareSection("public", "/mnt/data/public", Settings{{"guest ok", "no"}}))
		contains(t, updated, "[public]\n   path = /mnt/data/public\n   guest ok = no",
			"# Sambaconfig, hand-edited 2024", "   # player needs this", "valid users = admin")
	})
	t.Run("does not swallow the section that follows the one it edits", func(t *testing.T) {
		updated := UpsertSection(smbConf, "public", ShareSection("public", "/x", nil))
		if !has(updated, "admin") || !has(updated, "global") {
			t.Errorf("lost a section: %q", updated)
		}
	})
	t.Run("appends a share the file does not have", func(t *testing.T) {
		updated := UpsertSection(smbConf, "media", ShareSection("media", "/mnt/data/media", Settings{{"read only", "yes"}}))
		if !has(updated, "media") || !has(updated, "public") {
			t.Errorf("got %q", updated)
		}
	})
	t.Run("gives the same file whether it runs once or twice", func(t *testing.T) {
		section := ShareSection("public", "/mnt/data", Settings{{"guest ok", "yes"}})
		once := UpsertSection(smbConf, "public", section)
		if twice := UpsertSection(once, "public", section); twice != once {
			t.Errorf("twice %q != once %q", twice, once)
		}
	})
	t.Run("puts path first, since it is the line anybody looks for", func(t *testing.T) {
		if got := ShareSection("x", "/p", Settings{{"read only", "no"}}); got != "[x]\n   path = /p\n   read only = no" {
			t.Errorf("ShareSection = %q", got)
		}
	})
}

func TestRemoveSection(t *testing.T) {
	t.Run("takes out the share and nothing else", func(t *testing.T) {
		without := RemoveSection(smbConf, "public")
		if has(without, "public") || !has(without, "admin") {
			t.Errorf("got %q", without)
		}
		contains(t, without, "# player needs this")
	})
	t.Run("leaves a file alone when the share is not in it", func(t *testing.T) {
		if got := RemoveSection(smbConf, "media"); got != smbConf {
			t.Errorf("got %q", got)
		}
	})
}

func TestParseSambaUsers(t *testing.T) {
	check := func(t *testing.T, in string, want []string) {
		t.Helper()
		if got := ParseSambaUsers(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseSambaUsers(%q) = %q, want %q", in, got, want)
		}
	}
	t.Run("takes the name out of pdbedit output", func(t *testing.T) {
		check(t, "admin:1000:\n", []string{"admin"})
	})
	t.Run("reads several, and ignores the blank line at the end", func(t *testing.T) {
		check(t, "admin:1000:\nmedia:1001:\n\n", []string{"admin", "media"})
	})
	t.Run("says nothing on a machine with no samba accounts", func(t *testing.T) {
		check(t, "", []string{})
	})
}

func global(text string) Settings {
	return ParseShareSettings(ParseSections(text)["global"])
}

func TestUpsertSetting(t *testing.T) {
	t.Run("replaces a key that is already there", func(t *testing.T) {
		if v, _ := global(UpsertSetting(smbConf, "global", "workgroup", "HOMELAB")).Get("workgroup"); v != "HOMELAB" {
			t.Errorf("workgroup = %q", v)
		}
	})
	t.Run("leaves every other setting in the section alone", func(t *testing.T) {
		updated := UpsertSetting(smbConf, "global", "netbios name", "HOMELAB")
		contains(t, updated, "server min protocol = SMB2", "   # player needs this")
		if !has(updated, "public") {
			t.Error("lost public")
		}
	})
	t.Run("adds a key the section does not have", func(t *testing.T) {
		if v, _ := global(UpsertSetting(smbConf, "global", "netbios name", "HOMELAB")).Get("netbios name"); v != "HOMELAB" {
			t.Errorf("netbios name = %q", v)
		}
	})
	t.Run("matches a key however it was spaced or capitalised", func(t *testing.T) {
		updated := UpsertSetting("[global]\n   Netbios   Name   =   OLD\n", "global", "netbios name", "NEW")
		contains(t, updated, "netbios name = NEW")
		lacks(t, updated, "OLD")
	})
	t.Run("does not touch a commented-out setting of the same name", func(t *testing.T) {
		contains(t, UpsertSetting("[global]\n   # netbios name = OLD\n", "global", "netbios name", "NEW"), "# netbios name = OLD")
	})
	t.Run("gives the same file whether it runs once or twice", func(t *testing.T) {
		once := UpsertSetting(smbConf, "global", "netbios name", "HOMELAB")
		if twice := UpsertSetting(once, "global", "netbios name", "HOMELAB"); twice != once {
			t.Errorf("twice %q != once %q", twice, once)
		}
	})
	t.Run("removes one key and leaves the section standing", func(t *testing.T) {
		without := RemoveSetting(smbConf, "global", "workgroup")
		if !has(without, "global") {
			t.Error("lost global")
		}
		contains(t, without, "server min protocol = SMB2")
		lacks(t, without, "workgroup")
	})
}

func TestParseShareRead(t *testing.T) {
	const out = "[chris]\n   path = /p\n#pulumi-homelab#parses\nok\n"
	path := map[string]string{"path": "/p"}
	t.Run("splits the file from the verdict", func(t *testing.T) {
		got := ParseShareRead(out, "chris")
		if !got.Found || !reflect.DeepEqual(got.Actual.Map(), path) || !got.Parses {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("reports a configuration samba cannot parse", func(t *testing.T) {
		if ParseShareRead("[chris]\n   path = /p\n#pulumi-homelab#parses\n", "chris").Parses {
			t.Error("parses")
		}
	})
	t.Run("still reads the section out of a file that does not parse", func(t *testing.T) {
		got := ParseShareRead("[chris]\n   path = /p\n#pulumi-homelab#parses\n", "chris")
		if !got.Found || !reflect.DeepEqual(got.Actual.Map(), path) {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("does not mistake the marker for part of the file", func(t *testing.T) {
		if _, ok := ParseShareRead(out, "chris").Actual.Get("#pulumi-homelab#parses"); ok {
			t.Error("marker read as a key")
		}
	})
}

func TestFileSetting(t *testing.T) {
	const file = "[global]\n   netbios name = homelab\n   workgroup = WORKGROUP\n"
	check := func(t *testing.T, share, key, want string, wantOK bool) {
		t.Helper()
		if got, ok := FileSetting(file, share, key); got != want || ok != wantOK {
			t.Errorf("FileSetting(%q, %q) = %q, %v; want %q, %v", share, key, got, ok, want, wantOK)
		}
	}
	t.Run("finds it by key, however it was spaced", func(t *testing.T) {
		check(t, "global", "netbios   name", "homelab", true)
		check(t, "global", "NetBIOS name", "homelab", true)
	})
	t.Run("answers nothing for a key the section does not have", func(t *testing.T) {
		check(t, "global", "server string", "", false)
	})
	t.Run("answers nothing for a section the file does not have", func(t *testing.T) {
		check(t, "chris", "netbios name", "", false)
	})
	t.Run("reads back what was declared rather than what samba uppercased it to", func(t *testing.T) {
		check(t, "global", "netbios name", "homelab", true)
	})
}
