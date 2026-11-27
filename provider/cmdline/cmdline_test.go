package cmdline

import (
	"strings"
	"testing"
)

const real = "console=serial0,115200 console=tty1 root=PARTUUID=00112233-02 rootfstype=ext4 fsck.repair=yes rootwait"

func is(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("%q does not contain %q", s, sub)
	}
}

func TestMergeCmdline(t *testing.T) {
	t.Run("appends what is missing and leaves everything else exactly as it was", func(t *testing.T) {
		is(t, Merge(real, []string{"cgroup_memory=1", "cgroup_enable=memory"}), real+" cgroup_memory=1 cgroup_enable=memory")
	})
	t.Run("changes nothing when the parameters are already there", func(t *testing.T) {
		already := real + " cgroup_memory=1"
		is(t, Merge(already, []string{"cgroup_memory=1"}), already)
	})
	t.Run("corrects a parameter that is present with the wrong value", func(t *testing.T) {
		is(t, Merge(real+" cgroup_memory=0", []string{"cgroup_memory=1"}), real+" cgroup_memory=1")
	})
	t.Run("corrects in place, so the ordering the image was written with survives", func(t *testing.T) {
		is(t, Merge("cgroup_memory=0 root=PARTUUID=00112233-02 rootwait", []string{"cgroup_memory=1"}), "cgroup_memory=1 root=PARTUUID=00112233-02 rootwait")
	})
	t.Run("gives the same line whether it runs once or twice", func(t *testing.T) {
		once := Merge(real+" cgroup_memory=0", []string{"cgroup_memory=1", "cgroup_enable=memory"})
		is(t, Merge(once, []string{"cgroup_memory=1", "cgroup_enable=memory"}), once)
	})
	t.Run("handles a bare parameter, which has no value to compare", func(t *testing.T) {
		is(t, Merge("quiet rootwait", []string{"quiet"}), "quiet rootwait")
		is(t, Merge("rootwait", []string{"quiet"}), "rootwait quiet")
	})
	t.Run("does not mistake one parameter for another that starts the same way", func(t *testing.T) {
		is(t, Merge("cgroup_enable=cpuset", []string{"cgroup_enable_memory=1"}), "cgroup_enable=cpuset cgroup_enable_memory=1")
	})
	t.Run("collapses the whitespace a hand edit leaves behind, without dropping anything", func(t *testing.T) {
		is(t, Merge("  quiet   rootwait  ", []string{"splash"}), "quiet rootwait splash")
	})
}

func TestBootedWith(t *testing.T) {
	t.Run("anchors on both sides, so cgroup_memory=0 does not satisfy cgroup_memory=1", func(t *testing.T) {
		is(t, BootedWith([]string{"cgroup_memory=1"}), `grep -qE '(^| )cgroup_memory=1( |$)' /proc/cmdline`)
	})
	t.Run("joins several parameters so all of them have to hold", func(t *testing.T) {
		contains(t, BootedWith([]string{"a=1", "b=2"}), " && ")
	})
	t.Run("treats a parameter as text rather than as a pattern", func(t *testing.T) {
		contains(t, BootedWith([]string{"a.b=1"}), `a\.b=1`)
		contains(t, BootedWith([]string{"a+b"}), `a\+b`)
	})
}
