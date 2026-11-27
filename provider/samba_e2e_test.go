package homelab

import (
	"strings"
	"sync"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	presource "github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"

	"github.com/antimatter-studios/pulumi-homelab/provider/remote/remotetest"
)

var (
	shareURN   = presource.URN("urn:pulumi:s::p::homelab:index:SambaShare::photos")
	settingURN = presource.URN("urn:pulumi:s::p::homelab:index:SambaSetting::netbios")
	sambaURN   = presource.URN("urn:pulumi:s::p::homelab:index:SambaUser::chris")
)

// fakeSamba is a machine with one smb.conf, answering the commands the samba resources run.
type fakeSamba struct {
	mu      sync.Mutex
	conf    string
	parses  bool
	applied int
	reloads []string
}

// unescalate undoes asRoot: sudo -n sh -c '<command>'.
func unescalate(c string) string {
	inner := strings.TrimSuffix(strings.TrimPrefix(c, "sudo -n sh -c '"), "'")
	return strings.ReplaceAll(inner, `'\''`, "'")
}

func (f *fakeSamba) runner() *remotetest.Runner {
	r := remotetest.New()
	r.When(func(string) bool { return true }, func(c string) remotetest.Reply {
		f.mu.Lock()
		defer f.mu.Unlock()
		cmd := unescalate(c)
		switch {
		case cmd == "cat '/etc/samba/smb.conf'":
			return remotetest.Reply{Stdout: f.conf}
		case cmd == readShareSectionCommand(smbConf):
			verdict := ""
			if f.parses {
				verdict = "ok\n"
			}
			return remotetest.Reply{Stdout: f.conf + "\n#pulumi-homelab#parses\n" + verdict}
		case cmd == testparmCommand(smbConf):
			return remotetest.Reply{Stdout: f.conf}
		case strings.HasPrefix(cmd, "staging=$(mktemp) && cat > \"$staging\" <<'PULUMI_EOF'\n"):
			body := strings.TrimPrefix(cmd, "staging=$(mktemp) && cat > \"$staging\" <<'PULUMI_EOF'\n")
			body = body[:strings.Index(body, "PULUMI_EOF\n")]
			if cmd != sambaApplyCommand(body, smbConf) && cmd != sambaApplyCommand(strings.TrimSuffix(body, "\n"), smbConf) {
				return remotetest.Reply{Code: 99, Stderr: "unexpected apply command"}
			}
			f.conf, f.applied = body, f.applied+1
			return remotetest.Reply{}
		case strings.HasPrefix(cmd, "smbcontrol") || strings.HasPrefix(cmd, "systemctl"):
			f.reloads = append(f.reloads, cmd)
			return remotetest.Reply{}
		}
		return remotetest.Reply{Code: 127, Stderr: "unscripted: " + cmd}
	}, 0)
	return r
}

const baseConf = "[global]\n   workgroup = WORKGROUP\n\n[homes]\n   browseable = no\n"

func TestSambaShareLifecycle(t *testing.T) {
	f := &fakeSamba{conf: baseConf, parses: true}
	fake := f.runner()
	s := configured(t, fake)
	inputs := property.NewMap(map[string]property.Value{
		"share": property.New("photos"), "path": property.New("/mnt/photos"), "config": property.New(smbConf),
		"settings": property.New(property.NewMap(map[string]property.Value{"read only": property.New("no"), "guest ok": property.New("no")})),
	})
	created, err := s.Create(p.CreateRequest{Urn: shareURN, Properties: inputs})
	if err != nil {
		t.Fatal(err)
	}
	// the settings are written in the order the TypeScript saw them: sorted, as protobuf delivered them
	if !strings.Contains(f.conf, "[photos]\n   path = /mnt/photos\n   guest ok = no\n   read only = no") || !strings.HasPrefix(f.conf, baseConf) {
		t.Fatalf("conf %q", f.conf)
	}
	if created.ID != "photos" || !created.Properties.Get("parses").AsBool() || created.Properties.Get("actual").AsMap().Get("path").AsString() != "/mnt/photos" {
		t.Fatalf("created %v", created.Properties)
	}

	// adopting a section that already says it writes nothing. (A section that is last in the file is
	// rewritten every time, because upsertSection takes the file's final newline into the section and
	// the heredoc puts it back; that is the TypeScript's behaviour, kept. Here another section follows.)
	f.conf += "[after]\n   x = y\n"
	applied := f.applied
	if _, err := s.Create(p.CreateRequest{Urn: shareURN, Properties: inputs}); err != nil || f.applied != applied {
		t.Fatalf("adopting rewrote the file: %v", err)
	}

	// import: no diff against the declaration
	imported, err := s.Read(p.ReadRequest{ID: "photos", Urn: shareURN})
	if err != nil || imported.Inputs.Get("path").AsString() != "/mnt/photos" || imported.Inputs.Get("settings").AsMap().Get("guest ok").AsString() != "no" {
		t.Fatalf("import %+v %v", imported, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "photos", Urn: shareURN, State: imported.Properties, Inputs: inputs}); err != nil || diff.HasChanges {
		t.Fatalf("import diff %+v %v", diff, err)
	}

	// a hand edit is drift; a file samba cannot parse is a change; a new name replaces, deleting first
	f.conf = strings.Replace(f.conf, "guest ok = no", "guest ok = yes", 1)
	read, err := s.Read(p.ReadRequest{ID: "photos", Urn: shareURN, Properties: created.Properties, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "photos", Urn: shareURN, State: read.Properties, Inputs: inputs}); err != nil || diff.DetailedDiff["settings"].Kind != p.Update {
		t.Fatalf("hand edit %+v %v", diff, err)
	}
	f.parses = false
	unparsed, _ := s.Read(p.ReadRequest{ID: "photos", Urn: shareURN, Properties: created.Properties, Inputs: inputs})
	if diff, err := s.Diff(p.DiffRequest{ID: "photos", Urn: shareURN, State: unparsed.Properties, Inputs: inputs}); err != nil || !diff.HasChanges {
		t.Fatalf("unparsed %+v %v", diff, err)
	}
	f.parses = true
	renamed := property.NewMap(map[string]property.Value{"share": property.New("pictures"), "path": property.New("/mnt/photos")})
	if diff, err := s.Diff(p.DiffRequest{ID: "photos", Urn: shareURN, State: created.Properties, Inputs: renamed}); err != nil || !diff.DeleteBeforeReplace || diff.DetailedDiff["share"].Kind != p.UpdateReplace {
		t.Fatalf("rename %+v %v", diff, err)
	}

	// update puts the declaration back
	if _, err := s.Update(p.UpdateRequest{ID: "photos", Urn: shareURN, State: read.Properties, Inputs: inputs}); err != nil || !strings.Contains(f.conf, "guest ok = no") {
		t.Fatalf("update %v: %q", err, f.conf)
	}

	// delete removes its own section and nothing else
	if err := s.Delete(p.DeleteRequest{ID: "photos", Urn: shareURN, Properties: created.Properties}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.conf, "photos") || !strings.Contains(f.conf, "[global]\n   workgroup = WORKGROUP") || !strings.Contains(f.conf, "[homes]\n   browseable = no") {
		t.Fatalf("after delete %q", f.conf)
	}
	if gone, err := s.Read(p.ReadRequest{ID: "photos", Urn: shareURN, Properties: created.Properties}); err != nil || gone.ID != "" {
		t.Fatalf("read after delete %+v %v", gone, err)
	}
}

func TestSambaSettingLifecycle(t *testing.T) {
	f := &fakeSamba{conf: baseConf, parses: true}
	s := configured(t, f.runner())
	inputs := func(value, apply string) property.Map {
		return property.NewMap(map[string]property.Value{
			"share": property.New("global"), "key": property.New("netbios name"), "value": property.New(value),
			"apply": property.New(apply), "config": property.New(smbConf),
		})
	}
	created, err := s.Create(p.CreateRequest{Urn: settingURN, Properties: inputs("homelab", "nmbd")})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "global#netbios name" || !strings.Contains(f.conf, "[global]\n   workgroup = WORKGROUP\n\n   netbios name = homelab\n[homes]") {
		t.Fatalf("created %s %q", created.ID, f.conf)
	}
	if len(f.reloads) != 1 || f.reloads[0] != sambaApply["nmbd"] {
		t.Fatalf("reloads %v", f.reloads)
	}
	// samba uppercases a netbios name; that is the same value
	f.conf = strings.Replace(f.conf, "= homelab", "= HOMELAB", 1)
	read, err := s.Read(p.ReadRequest{ID: created.ID, Urn: settingURN, Properties: created.Properties, Inputs: inputs("homelab", "nmbd")})
	if err != nil || read.Properties.Get("actual").AsString() != "HOMELAB" || read.Properties.Get("effective").AsString() != "HOMELAB" {
		t.Fatalf("read %+v %v", read, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: read.Properties, Inputs: inputs("homelab", "nmbd")}); err != nil || diff.HasChanges {
		t.Fatalf("case diff %+v %v", diff, err)
	}
	// import gives the declared spelling when the file means the same
	imported, err := s.Read(p.ReadRequest{ID: created.ID, Urn: settingURN, Inputs: inputs("homelab", "nmbd")})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: imported.Properties, Inputs: inputs("homelab", "nmbd")}); err != nil || diff.HasChanges {
		t.Fatalf("import diff %+v %v", diff, err)
	}
	// an ID-only import, as the #38 switch does it, cannot know apply: samba-netbios-name declares
	// nmbd, and that must not diff
	bare, err := s.Read(p.ReadRequest{ID: created.ID, Urn: settingURN})
	if err != nil {
		t.Fatalf("id-only import %v", err)
	}
	if v, ok := bare.Properties.GetOk("apply"); ok && !v.IsNull() {
		t.Fatalf("an id-only import claimed to know apply: %v", v)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: bare.Properties, Inputs: inputs("homelab", "nmbd")}); err != nil {
		t.Fatalf("id-only import diff %v", err)
	} else if _, applies := diff.DetailedDiff["apply"]; applies {
		t.Fatalf("id-only import diffs on apply: %+v", diff)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: read.Properties, Inputs: inputs("other", "nmbd")}); err != nil || diff.DetailedDiff["value"].Kind != p.Update {
		t.Fatalf("value diff %+v %v", diff, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: read.Properties, Inputs: inputs("homelab", "all")}); err != nil || diff.DetailedDiff["apply"].Kind != p.Update {
		t.Fatalf("apply diff %+v %v", diff, err)
	}
	moved := property.NewMap(map[string]property.Value{"share": property.New("global"), "key": property.New("workgroup"), "value": property.New("x")})
	if diff, err := s.Diff(p.DiffRequest{ID: created.ID, Urn: settingURN, State: read.Properties, Inputs: moved}); err != nil || !diff.DeleteBeforeReplace || diff.DetailedDiff["key"].Kind != p.UpdateReplace {
		t.Fatalf("key diff %+v %v", diff, err)
	}
	// delete removes its own line and nothing else
	if err := s.Delete(p.DeleteRequest{ID: created.ID, Urn: settingURN, Properties: read.Properties}); err != nil {
		t.Fatal(err)
	}
	if f.conf != baseConf {
		t.Fatalf("after delete %q", f.conf)
	}
	if gone, err := s.Read(p.ReadRequest{ID: created.ID, Urn: settingURN, Properties: read.Properties}); err != nil || gone.ID != "" {
		t.Fatalf("read after delete %+v %v", gone, err)
	}
}

// Two resources editing one smb.conf in parallel both land: the read-edit-write is serialised.
func TestSambaUserLifecycle(t *testing.T) {
	setPw := asRoot("smbpasswd -s -a 'chris' >/dev/null")
	known := false
	fake := remotetest.New().
		On("getent passwd 'chris'", remotetest.Reply{Stdout: "chris:x:1000:1000::/home/chris:/bin/bash\n"}).
		On("getent passwd 'nobody2'", remotetest.Reply{Code: 2}).
		On(`sudo -n sh -c 'smbpasswd -x '\''chris'\'' >/dev/null 2>&1 || true'`, remotetest.Reply{})
	fake.When(func(c string) bool { return c == setPw }, func(string) remotetest.Reply { known = true; return remotetest.Reply{} }, 0)
	fake.When(func(c string) bool { return c == `sudo -n sh -c 'pdbedit -L 2>/dev/null'` }, func(string) remotetest.Reply {
		if !known {
			return remotetest.Reply{}
		}
		return remotetest.Reply{Stdout: "chris:1000:\n"}
	}, 0)
	s := configured(t, fake)
	in := func(name, pw string) property.Map {
		return property.NewMap(map[string]property.Value{"name": property.New(name), "password": property.New(pw).WithSecret(true)})
	}
	if _, err := s.Create(p.CreateRequest{Urn: sambaURN, Properties: in("nobody2", "x")}); err == nil || !strings.Contains(err.Error(), "there is no unix account called nobody2; samba maps onto one, so declare a User first") {
		t.Fatalf("no unix account: %v", err)
	}
	created, err := s.Create(p.CreateRequest{Urn: sambaURN, Properties: in("chris", "s3cret")})
	if err != nil || !created.Properties.Get("exists").AsBool() || !created.Properties.Get("password").IsSecret() {
		t.Fatalf("create %+v %v", created, err)
	}
	read, err := s.Read(p.ReadRequest{ID: "chris", Urn: sambaURN, Properties: created.Properties})
	if err != nil || read.ID != "chris" {
		t.Fatalf("read %+v %v", read, err)
	}
	imported, err := s.Read(p.ReadRequest{ID: "chris", Urn: sambaURN, Inputs: in("chris", "s3cret")})
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "chris", Urn: sambaURN, State: imported.Properties, Inputs: in("chris", "s3cret")}); err != nil || diff.HasChanges {
		t.Fatalf("import diff %+v %v", diff, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "chris", Urn: sambaURN, State: created.Properties, Inputs: in("chris", "new")}); err != nil || diff.DetailedDiff["password"].Kind != p.Update {
		t.Fatalf("password diff %+v %v", diff, err)
	}
	if diff, err := s.Diff(p.DiffRequest{ID: "chris", Urn: sambaURN, State: created.Properties, Inputs: in("alice", "s3cret")}); err != nil || !diff.DeleteBeforeReplace || diff.DetailedDiff["name"].Kind != p.UpdateReplace {
		t.Fatalf("name diff %+v %v", diff, err)
	}
	if err := s.Delete(p.DeleteRequest{ID: "chris", Urn: sambaURN, Properties: created.Properties}); err != nil || !fake.Ran("smbpasswd -x") {
		t.Fatalf("delete %v", err)
	}
	// a failure does not put the password into the error
	failing := remotetest.New().On("getent passwd 'chris'", remotetest.Reply{}).On(setPw, remotetest.Reply{Code: 1, Stderr: "Failed to add entry for user chris.\n"})
	_, err = configured(t, failing).Create(p.CreateRequest{Urn: sambaURN, Properties: in("chris", "s3cret")})
	if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "smbpasswd failed for user chris") ||
		!strings.Contains(err.Error(), "(exit 1)") || strings.Contains(err.Error(), "Failed to add entry") {
		t.Fatalf("failure %v", err)
	}
}

// smbpasswd's output never reaches an error, whatever form the password takes in it: padded,
// with a tab, with CRLF, or one line of a multiline password.
func TestSambaUserErrorNeverCarriesThePassword(t *testing.T) {
	for _, password := range []string{"  padded  ", "tab\there", "crlf\r\npart", "line one\nline two"} {
		echo := "smbpasswd: " + password + "\n" + strings.TrimSpace(password) + "\r\n" +
			strings.ReplaceAll(password, "\t", "\\t") + "\n" + strings.Split(password, "\n")[0] + "\n"
		fake := remotetest.New().On("getent passwd 'chris'", remotetest.Reply{}).
			When(func(c string) bool { return strings.Contains(c, "smbpasswd -s -a") }, func(string) remotetest.Reply { return remotetest.Reply{Code: 1, Stdout: echo, Stderr: echo} }, 0)
		in := property.NewMap(map[string]property.Value{"name": property.New("chris"), "password": property.New(password).WithSecret(true)})
		_, err := configured(t, fake).Create(p.CreateRequest{Urn: sambaURN, Properties: in})
		if err == nil || !strings.Contains(err.Error(), "smbpasswd failed for user chris") || !strings.Contains(err.Error(), "(exit 1)") {
			t.Fatalf("%q: want smbpasswd's exit-1 failure, got %v", password, err)
		}
		for _, piece := range []string{"padded", "tab", "here", "crlf", "part", "line one", "line two"} {
			if strings.Contains(err.Error(), piece) {
				t.Fatalf("%q: the error carries %q: %v", password, piece, err)
			}
		}
	}
}

// The #38 switch imports samba-user-chris; samba cannot give the password back, so an import
// leaves it unknown and the declared password diffs clean.
func TestSambaUserImportIgnoresThePassword(t *testing.T) {
	fake := remotetest.New().When(func(c string) bool { return strings.Contains(c, "pdbedit -L") },
		func(string) remotetest.Reply { return remotetest.Reply{Stdout: "chris:1000:\n"} }, 0)
	s := configured(t, fake)
	imported, err := s.Read(p.ReadRequest{ID: "chris", Urn: sambaURN})
	if err != nil || imported.ID != "chris" {
		t.Fatalf("import read %+v %v", imported, err)
	}
	declared := property.NewMap(map[string]property.Value{"name": property.New("chris"), "password": property.New("s3cret").WithSecret(true)})
	if d, err := s.Diff(p.DiffRequest{ID: "chris", Urn: sambaURN, State: imported.Properties, Inputs: declared}); err != nil || d.HasChanges {
		t.Fatalf("import diff %+v %v", d, err)
	}
}
