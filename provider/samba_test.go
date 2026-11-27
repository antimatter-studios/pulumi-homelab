package homelab

import (
	"reflect"
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/samba"
)

func effective(t *testing.T, text, share string) samba.Settings {
	t.Helper()
	s, ok := samba.EffectiveShare(text, share)
	if !ok {
		t.Fatalf("no [%s]", share)
	}
	return s
}

func get(s samba.Settings, key string) string { v, _ := s.Get(key); return v }

// Ported from src/resources/samba.test.ts › comparing a declared value with the one Samba reports.
func TestSambaSameValue(t *testing.T) {
	t.Run("accepts the capitalisation testparm prints", func(t *testing.T) {
		if !sambaSameValue("guest ok", "yes", "Yes") || !sambaSameValue("read only", "no", "No") {
			t.Fatal("rejected")
		}
	})
	t.Run("accepts every spelling of a boolean Samba accepts", func(t *testing.T) {
		for _, c := range [][2]string{{"yes", "true"}, {"1", "Yes"}, {"no", "false"}, {"0", "No"}} {
			if !sambaSameValue("guest ok", c[0], c[1]) {
				t.Errorf("%s/%s", c[0], c[1])
			}
		}
	})
	t.Run("still reports a real difference", func(t *testing.T) {
		if sambaSameValue("guest ok", "yes", "No") || sambaSameValue("valid users", "alice", "bob") {
			t.Fatal("accepted")
		}
	})
	t.Run("accepts the uppercasing Samba applies to a netbios name", func(t *testing.T) {
		if !sambaSameValue("netbios name", "homelab", "HOMELAB") || !sambaSameValue("workgroup", "WORKGROUP", "workgroup") {
			t.Fatal("rejected")
		}
	})
	t.Run("compares a path exactly, because case matters in one", func(t *testing.T) {
		if sambaSameValue("path", "/mnt/data", "/Mnt/Data") {
			t.Fatal("accepted")
		}
	})
	t.Run("is unmoved by whitespace around a value", func(t *testing.T) {
		if !sambaSameValue("path", "/mnt/data", "  /mnt/data  ") {
			t.Fatal("rejected")
		}
	})
}

const testparmV = "[global]\n\tnetbios name = HOMELAB\n\tworkgroup = WORKGROUP\n\tserver string = Samba\n\tlog level = 0\n\tmax log size = 1000\n\tdeadtime = 10080\n\n" +
	"[public]\n\tpath = /mnt/data\n\tguest ok = Yes\n\tread only = Yes\n\tbrowseable = Yes\n\tcreate mask = 0744\n"

// Ported from src/resources/samba.test.ts › a setting that equals its own default.
func TestNarrowTo(t *testing.T) {
	t.Run("is visible in the -v output where -s omitted it", func(t *testing.T) {
		if got := get(effective(t, testparmV, "global"), "netbios name"); got != "HOMELAB" {
			t.Fatal(got)
		}
	})
	t.Run("compares equal to the declared value, case-folded", func(t *testing.T) {
		if !sambaSameValue("netbios name", "homelab", get(effective(t, testparmV, "global"), "netbios name")) {
			t.Fatal("rejected")
		}
	})
	t.Run("keeps only the keys somebody asked about", func(t *testing.T) {
		got := narrowTo(effective(t, testparmV, "public"), []string{"path", "guest ok"})
		if !reflect.DeepEqual(got.Map(), map[string]string{"path": "/mnt/data", "guest ok": "Yes"}) {
			t.Fatal(got)
		}
	})
	t.Run("matches a key however it was spaced or capitalised when narrowing", func(t *testing.T) {
		got := narrowTo(effective(t, testparmV, "global"), []string{"Netbios   Name"})
		if !reflect.DeepEqual(got.Map(), map[string]string{"netbios name": "HOMELAB"}) {
			t.Fatal(got)
		}
	})
	t.Run("leaves out a key Samba does not report at all", func(t *testing.T) {
		if got := narrowTo(effective(t, testparmV, "global"), []string{"not a real parameter"}); len(got) != 0 {
			t.Fatal(got)
		}
	})
	t.Run("reports no disagreement for a declared value that resolved to its default", func(t *testing.T) {
		e := effective(t, testparmV, "public")
		if !sambaSameValue("guest ok", "yes", get(e, "guest ok")) || !sambaSameValue("read only", "yes", get(e, "read only")) {
			t.Fatal("disagrees")
		}
	})
}

// Ported from src/resources/samba.test.ts › asking testparm the right question.
func TestTestparmCommand(t *testing.T) {
	t.Run("includes defaults, which is what makes a setting equal to its default visible", func(t *testing.T) {
		if !strings.Contains(testparmCommand("/etc/samba/smb.conf"), "-sv") {
			t.Fatal(testparmCommand("/etc/samba/smb.conf"))
		}
	})
	t.Run("suppresses the prompt, or testparm waits for a keypress nobody can give it", func(t *testing.T) {
		if !strings.Contains(testparmCommand(smbConf), "-s") {
			t.Fatal(testparmCommand(smbConf))
		}
	})
	t.Run("quotes the path, which may be anywhere", func(t *testing.T) {
		if !strings.Contains(testparmCommand("/etc/it's/smb.conf"), `'\''`) {
			t.Fatal(testparmCommand("/etc/it's/smb.conf"))
		}
	})
	t.Run("sends testparm’s commentary to nowhere, since it is not the answer", func(t *testing.T) {
		if !strings.Contains(testparmCommand(smbConf), "2>/dev/null") {
			t.Fatal(testparmCommand(smbConf))
		}
	})
	t.Run("is exactly the command the TypeScript ran", func(t *testing.T) {
		if got := testparmCommand(smbConf); got != "testparm -sv '/etc/samba/smb.conf' 2>/dev/null" {
			t.Fatal(got)
		}
	})
}

// Ported from src/resources/samba.test.ts › comparing against the file rather than against testparm.
func TestShareDiffers(t *testing.T) {
	declared := wantedShare("/mnt/pool/chris", map[string]string{
		"writeable": "yes", "directory mask": "2775", "guest ok": "no", "vfs objects": "catia fruit streams_xattr",
	})
	file := strings.Join([]string{
		"[global]", "   vfs objects = catia fruit streams_xattr", "", "[chris]", "   path = /mnt/pool/chris", "   writeable = yes",
		"   directory mask = 2775", "   guest ok = no", "   vfs objects = catia fruit streams_xattr",
	}, "\n")
	testparm := strings.Join([]string{"[chris]", "   path = /mnt/pool/chris", "   read only = No", "   directory mask = 02775"}, "\n")
	fileShare := func(text string) samba.Settings {
		s, _ := samba.FileShare(text, "chris")
		return s
	}
	t.Run("finds the section in the file", func(t *testing.T) {
		want := map[string]string{"path": "/mnt/pool/chris", "writeable": "yes", "directory mask": "2775", "guest ok": "no", "vfs objects": "catia fruit streams_xattr"}
		if got := fileShare(file); !reflect.DeepEqual(got.Map(), want) {
			t.Fatal(got)
		}
	})
	t.Run("says nothing about a section the file does not have", func(t *testing.T) {
		if _, ok := samba.FileShare(file, "photos"); ok {
			t.Fatal("found")
		}
	})
	t.Run("is quiet when the file says what was declared", func(t *testing.T) {
		if shareDiffers(fileShare(file), declared) {
			t.Fatal("differs")
		}
	})
	t.Run("would have reported a difference against testparm that does not exist", func(t *testing.T) {
		if !shareDiffers(effective(t, testparm, "chris"), declared) {
			t.Fatal("quiet")
		}
	})
	t.Run("does not mind a synonym, because it never sees one", func(t *testing.T) {
		if _, ok := fileShare(file).Get("writeable"); !ok {
			t.Fatal("no writeable")
		}
		if _, ok := effective(t, testparm, "chris").Get("read only"); !ok {
			t.Fatal("no read only")
		}
	})
	t.Run("does not mind a setting that matches [global], because the file still carries it", func(t *testing.T) {
		if get(fileShare(file), "vfs objects") != "catia fruit streams_xattr" {
			t.Fatal("missing")
		}
		if _, ok := effective(t, testparm, "chris").Get("vfs objects"); ok {
			t.Fatal("present")
		}
	})
	t.Run("does not mind a value equal to samba's default, because the file still carries it", func(t *testing.T) {
		if get(fileShare(file), "guest ok") != "no" {
			t.Fatal("missing")
		}
		if _, ok := effective(t, testparm, "chris").Get("guest ok"); ok {
			t.Fatal("present")
		}
	})
	t.Run("does not mind a normalised spelling, because the file was not normalised", func(t *testing.T) {
		if get(fileShare(file), "directory mask") != "2775" || get(effective(t, testparm, "chris"), "directory mask") != "02775" {
			t.Fatal("wrong")
		}
	})
	t.Run("reports a setting somebody changed by hand", func(t *testing.T) {
		if !shareDiffers(fileShare(strings.Replace(file, "writeable = yes", "writeable = no", 1)), declared) {
			t.Fatal("quiet")
		}
	})
	t.Run("reports a setting somebody deleted by hand", func(t *testing.T) {
		if !shareDiffers(fileShare(strings.Replace(file, "   guest ok = no\n", "", 1)), declared) {
			t.Fatal("quiet")
		}
	})
	t.Run("forgives a hand edit that changed the spelling and not the value", func(t *testing.T) {
		if shareDiffers(fileShare(strings.Replace(file, "guest ok = no", "guest ok = No", 1)), declared) {
			t.Fatal("differs")
		}
	})
	t.Run("forgives a hand edit that only changed how the key was spaced or capitalised", func(t *testing.T) {
		edited := strings.Replace(strings.Replace(file, "directory mask =", "directory  mask   =", 1), "guest ok =", "Guest Ok =", 1)
		if shareDiffers(fileShare(edited), declared) {
			t.Fatal("differs")
		}
	})
	t.Run("leaves a setting somebody added alone, since the section is edited in place", func(t *testing.T) {
		edited := strings.Replace(file, "   path = /mnt/pool/chris", "   path = /mnt/pool/chris\n   veto files = /.DS_Store/", 1)
		if shareDiffers(fileShare(edited), declared) {
			t.Fatal("differs")
		}
	})
}

func TestHeredocInto(t *testing.T) {
	if got := heredocInto(`"$staging"`, "a\nb"); got != "cat > \"$staging\" <<'PULUMI_EOF'\na\nb\nPULUMI_EOF\n" {
		t.Fatalf("%q", got)
	}
	if got := heredocInto("/x", "a\n", "chmod 0644 /x"); got != "cat > /x <<'PULUMI_EOF' && chmod 0644 /x\na\nPULUMI_EOF\n" {
		t.Fatalf("%q", got)
	}
}
