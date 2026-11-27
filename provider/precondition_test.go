package homelab

import (
	"strings"
	"testing"
)

// Ported from src/resources/precondition.test.ts: composing the check.

func TestCheckCommand(t *testing.T) {
	if got := checkCommand("grep -q cgroup_memory /proc/cmdline", false, ""); got != "grep -q cgroup_memory /proc/cmdline" {
		t.Fatal(got)
	}
}

func TestCheckCommandWrapsRootWhole(t *testing.T) {
	rooted := checkCommand("test -f /root/.ssh/authorized_keys && grep -q x /root/x", true, "")
	if !strings.HasPrefix(rooted, "sudo -n sh -c ") || !strings.Contains(rooted, "'test -f /root/.ssh/authorized_keys && grep -q x /root/x'") {
		t.Fatal(rooted)
	}
}

func TestCheckCommandIsStable(t *testing.T) {
	check := `awk '$1 == "memory"' /proc/cgroups`
	if checkCommand(check, true, "") != checkCommand(check, true, "") {
		t.Fatal("differs")
	}
	// and with the machine's own escalation, as create and read both send it
	if got := checkCommand("true", true, "none"); got != "true" {
		t.Fatal(got)
	}
}
