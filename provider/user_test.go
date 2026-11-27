package homelab

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/pulumi-homelab/provider/getent"
)

// Ported from src/resources/user.test.ts.

var realGroups = []string{
	"adm", "dialout", "cdrom", "sudo", "audio", "video", "plugdev", "games",
	"users", "input", "render", "netdev", "spi", "i2c", "gpio", "storage", "player",
}

func TestGroupsToLose(t *testing.T) {
	losing := groupsToLose(realGroups, []string{"audio", "video", "storage"})
	if !contains(losing, "sudo") || len(losing) != 14 {
		t.Errorf("names every membership the code does not mention: %v", losing)
	}
	if got := groupsToLose(realGroups, realGroups); len(got) != 0 {
		t.Errorf("says nothing when the declaration is the whole truth: %v", got)
	}
	if got := groupsToLose([]string{"audio"}, []string{"audio", "video", "newgroup"}); len(got) != 0 {
		t.Errorf("does not complain about a group being added: %v", got)
	}
	if got := groupsToLose([]string{"video", "audio"}, nil); strings.Join(got, ",") != "audio,video" {
		t.Errorf("sorts: %v", got)
	}
	if got := groupsToLose(nil, []string{"audio"}); len(got) != 0 {
		t.Errorf("handles an account in no groups at all: %v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func lockout(name, shell string, groups []string, allow bool, connectsAs *string, actual []string, exists bool) string {
	return lockoutRefusal(lockoutArgs{Name: name, Shell: shell, Groups: groups, AllowGroupRemoval: allow}, connectsAs, actual, exists)
}

func TestLockoutRefusal(t *testing.T) {
	admin := strp("admin")
	refusal := lockout("admin", "/usr/sbin/nologin", realGroups, false, admin, realGroups, true)
	if !strings.Contains(refusal, "the account this provider connects as") || !strings.Contains(refusal, "take away the login being used to apply it") {
		t.Errorf("refuses nologin for the connecting account: %q", refusal)
	}
	if got := lockout("backup", "/usr/sbin/nologin", realGroups, false, admin, []string{}, true); got != "" {
		t.Errorf("allows nologin on another account: %q", got)
	}
	if got := lockout("admin", "/usr/sbin/nologin", realGroups, false, nil, []string{}, true); got != "" {
		t.Errorf("allows it when the transport has no login: %q", got)
	}
	for _, shell := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false", "/usr/bin/false"} {
		if lockout("admin", shell, realGroups, false, admin, realGroups, true) == "" {
			t.Errorf("does not recognise %s", shell)
		}
	}
	if got := lockout("admin", "/bin/bash", []string{"audio", "video"}, false, admin, realGroups, true); !strings.Contains(got, "remove 15 memberships") {
		t.Errorf("counts what it would drop: %q", got)
	}
	if got := lockout("admin", "/bin/bash", []string{"audio"}, false, admin, realGroups, true); !strings.Contains(got, "including sudo") {
		t.Errorf("names sudo specially: %q", got)
	}
	if got := lockout("admin", "/bin/bash", []string{}, false, admin, []string{"audio"}, true); !strings.Contains(got, "remove 1 membership.") {
		t.Errorf("singular: %q", got)
	}
	if got := lockout("admin", "/bin/bash", []string{"audio"}, true, admin, realGroups, true); got != "" {
		t.Errorf("permits removal when meant: %q", got)
	}
	if got := lockout("admin", "/bin/bash", realGroups, false, admin, realGroups, true); got != "" {
		t.Errorf("nothing to refuse for the whole truth: %q", got)
	}
	if got := lockout("admin", "/bin/bash", []string{"audio"}, false, admin, nil, false); got != "" {
		t.Errorf("nothing to refuse for an account that does not exist yet: %q", got)
	}
}

// The exact wording, since the message is the product.
func TestLockoutRefusalExactText(t *testing.T) {
	want := "admin is in sudo, which the code does not mention. `groups` is the whole list rather than additions to it, so applying this would remove 1 membership, including sudo. Add them to the list, or set allowGroupRemoval: true if losing them is what you meant."
	if got := lockout("admin", "/bin/bash", []string{}, false, nil, []string{"sudo"}, true); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestUIDRefusal(t *testing.T) {
	if uidRefusal("t3code", intp(995), strp("995")) != "" || uidRefusal("t3code", nil, strp("995")) != "" || uidRefusal("t3code", intp(995), nil) != "" {
		t.Error("accepts a matching uid, or none declared, or no account")
	}
	refusal := uidRefusal("t3code", intp(995), strp("1001"))
	if !strings.Contains(refusal, "uid 1001") || !strings.Contains(refusal, "995") {
		t.Errorf("names both numbers: %q", refusal)
	}
	want := "t3code exists with uid 1001, and the code says 995. Renumbering an account leaves every file it owns outside its home owned by the old number, so this is refused. Change the files and the account by hand (usermod -u, then chown everything the old uid owned), or declare uid 1001."
	if refusal != want {
		t.Errorf("got\n%s\nwant\n%s", refusal, want)
	}
}

// #45: getent's uid field that Number() cannot read is NaN in the TypeScript, and the refusal said
// "exists with uid NaN". The port reproduces that message rather than panicking on the nil uid.
func TestUidRefusalForAnUnreadableUidSaysNaNAsTheTypeScriptDid(t *testing.T) {
	read := getent.ParseUserRead("odd", "odd:x:notanumber:100::/home/odd:/bin/sh\nusers\nusers\n")
	if read.UID != nil {
		t.Fatalf("uid %v", *read.UID)
	}
	refusal := uidRefusal("odd", intp(995), uidText(&read))
	if !strings.HasPrefix(refusal, "odd exists with uid NaN, and the code says 995.") {
		t.Fatalf("got %q", refusal)
	}
	if uidRefusal("odd", nil, uidText(&read)) != "" {
		t.Fatal("no uid declared is nothing to refuse")
	}
}

func TestUserCommandKeepsTheTypeScriptSpacing(t *testing.T) {
	got := userCommand(UserState{Name: "svc", Shell: "/usr/sbin/nologin", Home: "/home/svc", CreateHome: true, Groups: []string{}})
	want := "if getent passwd 'svc' >/dev/null; then usermod -s '/usr/sbin/nologin' -d '/home/svc'   'svc'; else useradd --system -m -s '/usr/sbin/nologin'   -d '/home/svc'  'svc'; fi"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	got = userCommand(UserState{Name: "t3code", Shell: "/bin/bash", Home: "/mnt/storage/t3code", Uid: intp(995), Group: strp("t3code"), Groups: []string{"a", "b"}})
	want = "if getent passwd 't3code' >/dev/null; then usermod -s '/bin/bash' -d '/mnt/storage/t3code' -g 't3code' -G 'a,b' 't3code'; else useradd --system -M -s '/bin/bash' -u 995 -g 't3code' -d '/mnt/storage/t3code' -G 'a,b' 't3code'; fi"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
