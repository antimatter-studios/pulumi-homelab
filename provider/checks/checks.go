// Package checks holds questions to hand to a Precondition: shell commands that exit zero when the
// machine is in a state the code requires. Ported from src/checks.ts.
package checks

import "github.com/antimatter-studios/pulumi-homelab/provider/shell"

// MountedAt is a command that succeeds only when path is a real mount point rather than an empty
// directory that looks like one. mountpoint is asked first; findmnt is the fallback for a stripped
// image that has one and not the other.
func MountedAt(path string) string {
	quoted := shell.Quote(path)
	return "mountpoint -q " + quoted + " 2>/dev/null || findmnt -rno TARGET " + quoted + " >/dev/null 2>&1"
}
