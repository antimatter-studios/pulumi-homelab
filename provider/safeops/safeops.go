// Package safeops removes one entry from a machine only when it is still exactly what a resource
// made, and never anything else, even something swapped in while the removal runs. It also
// computes the digests such an entry is checked against, so a resource records at apply time
// exactly what its delete will verify.
//
// The work is done by safeops.pl, run over the remote.Runner with perl, so that each step is one
// exact system call rather than a shell utility whose behaviour varies by version: coreutils mv,
// asked for no-clobber or no-copy, has been seen to fall back to a plain rename. perl-base is
// Essential on Debian; where perl is missing the step is refused, with no fallback.
//
// A removal:
//
//  1. pins the parent once: lstat says it is a real directory, chdir enters it, the directory
//     entered has the same dev:ino, and its physical path is the recorded parent. Only relative
//     names are used after that. A parent writable by group or others without the sticky bit is
//     refused, since no name in it can be trusted.
//  2. refuses an entry with a mount point at or under it (read from the mount table, which also
//     catches a bind mount of the same filesystem).
//  3. makes a private directory beside it, mode 0700, with a name from /dev/urandom, and checks
//     right after mkdir that it is that directory (owned by the caller, no group or other bits).
//  4. captures the entry with renameat2(AT_FDCWD, name, AT_FDCWD, private/entry,
//     RENAME_NOREPLACE): it never replaces and never copies. ENOSYS or EINVAL (no renameat2, or a
//     filesystem without RENAME_NOREPLACE) is refused, nothing touched. The private directory's
//     dev:ino is checked again after the capture.
//  5. verifies the captured entry only: a symlink's target exactly (readlink bytes), a file's
//     sha256 (opened with O_NOFOLLOW, fstat checked to be the entry lstat saw), a tree's manifest
//     sha256 (an lstat walk that never follows a symlink and never crosses st_dev), or that it is a
//     directory for EmptyDir. An empty or malformed expected value, a digest that cannot be
//     computed, and empty or malformed digest output all mean "not a match"; nothing is removed on
//     them.
//  6. on a match removes it: rmdir for EmptyDir, unlink for a file or link, and for a tree a
//     depth-first lstat walk of unlink and rmdir that never enters another st_dev. A walk that
//     stops reports Partial with the entries left and the first error.
//  7. on a mismatch restores it without replacing anything: link(2) then unlink(2) for a file or
//     link, renameat2(RENAME_NOREPLACE) for a directory. What cannot be put back stays in the
//     private directory.
//
// Every report names the parent, the staging name and the parent's dev:ino. An absolute path for
// what was left behind is given only while the parent is still the pinned directory; otherwise the
// report says the parent moved and where the entry is by staging name and dev:ino.
//
// Accepted limit (RULES.md): capturing a name does not stop a process that already has the file,
// or a directory in the tree, open from changing it after it was verified.
package safeops

import (
	_ "embed"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

//go:embed safeops.pl
var script string

// Kind is what the entry must be for it to count as the resource's.
type Kind string

const (
	// Symlink is a symbolic link whose target is exactly Expect.Value.
	Symlink Kind = "symlink"
	// File is a regular file whose sha256 is Expect.Value (see Digest).
	File Kind = "file"
	// Tree is a directory whose manifest sha256 is Expect.Value (see Digest), removed with
	// everything in it.
	Tree Kind = "tree"
	// EmptyDir is a directory removed with rmdir(2), so only while it is empty; Expect.Value is
	// unused.
	EmptyDir Kind = "emptydir"
)

// Expect is what the entry must be.
type Expect struct {
	Kind Kind
	// Value is the exact symlink target for Symlink, or the lowercase hex sha256 for File and Tree
	// as Digest computes it.
	Value string
}

// Outcome is what happened.
type Outcome string

const (
	// Absent: there was nothing at the path (or no parent), so nothing was done.
	Absent Outcome = "absent"
	// Removed: the entry was the resource's and is gone. Result.Path is set when the private
	// directory could not be removed afterwards.
	Removed Outcome = "removed"
	// Restored: the entry was not the resource's and is back at its path.
	Restored Outcome = "restored"
	// Kept: the entry was captured and is not back at its path; Result.Path (or Staging and
	// ParentDevIno when the parent moved) says where it is.
	Kept Outcome = "kept"
	// Partial: the entry was the resource's, and its removal stopped part way: Remaining entries
	// are left in the private directory, and Reason holds the first error.
	Partial Outcome = "partial"
	// Refused: nothing was touched, for the reason given.
	Refused Outcome = "refused"
	// Installed: Install put the file in place. Result.Path is set when something that was there
	// was moved aside and kept, or the staged copy could not be removed.
	Installed Outcome = "installed"
)

// Result is one removal's outcome.
type Result struct {
	Outcome Outcome
	// Path is where an entry was kept, or the private directory left behind, while the parent is
	// still where it was; "" when nothing was left or the parent moved.
	Path   string
	Reason string
	// Parent, Staging and ParentDevIno identify where anything left behind is, even when the
	// parent has moved: the staging directory's name under the directory with that dev:ino.
	Parent       string
	Staging      string
	ParentDevIno string
	// Remaining is how many entries a Partial removal left.
	Remaining int
}

// Split checks p is an absolute, clean path to an entry and returns its parent and name. An
// unclean path (/a/./b, /a/../b, //a, a trailing slash) is refused rather than cleaned: cleaning
// /a/../b without the filesystem can name a different entry than the kernel would, so a resource
// refuses such a path at Check and records Resolve's answer at Create.
func Split(p string) (parent, name string, err error) {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
		return "", "", fmt.Errorf("%q is not an absolute, clean path to an entry", p)
	}
	return path.Dir(p), path.Base(p), nil
}

func command(args ...string) string {
	quoted := []string{"perl", "-e", quote(script), "--"}
	for _, a := range args {
		quoted = append(quoted, quote(a))
	}
	return strings.Join(quoted, " ")
}

// Command is the shell command that removes the entry at p when it is as expected. The caller
// escalates it and passes what it prints to Parse. The expected value is checked by the script
// itself, so an empty or malformed one is refused there, with nothing touched.
func Command(p string, want Expect) (string, error) {
	parent, name, err := Split(p)
	if err != nil {
		return "", err
	}
	switch want.Kind {
	case Symlink, File, Tree, EmptyDir:
	default:
		return "", fmt.Errorf("unknown kind %q", want.Kind)
	}
	return command("remove", parent, name, string(want.Kind), want.Value), nil
}

// DigestCommand is the shell command that prints the digest of the entry at p as kind File or Tree
// checks it: the same code computes the digest a resource records at apply time and the one its
// delete verifies, so the two always agree.
func DigestCommand(p string, kind Kind) (string, error) {
	parent, name, err := Split(p)
	if err != nil {
		return "", err
	}
	if kind != File && kind != Tree {
		return "", fmt.Errorf("kind %q has no digest", kind)
	}
	return command("digest", parent, name, string(kind)), nil
}

// InstallMode says what Install may do to what is at the path.
type InstallMode string

const (
	// Create installs with link(2) after moving anything at the path aside with
	// renameat2(RENAME_NOREPLACE); nothing of anyone else's is replaced, and what was moved aside
	// is kept and reported unless it holds exactly the new content.
	Create InstallMode = "create"
	// Update replaces the declared file with rename(2): correcting drift in the resource's own file
	// is the intended act.
	Update InstallMode = "update"
)

// InstallCommand is the shell command that installs the content it reads on stdin at p, with mode
// (octal), owner and group (names or numbers). The parent must exist and be p's physical parent (see
// ResolveCommand); a directory at p is refused. The caller escalates it, sends the content alone on
// stdin, and passes what it prints to Parse: Installed, Kept (moved aside, then something appeared
// at p, so nothing was installed) or Refused.
func InstallCommand(p string, how InstallMode, mode, owner, group string) (string, error) {
	parent, name, err := Split(p)
	if err != nil {
		return "", err
	}
	if how != Create && how != Update {
		return "", fmt.Errorf("unknown install mode %q", how)
	}
	return command("install", parent, name, string(how), mode, owner, group), nil
}

// ResolveCommand is the shell command that prints p with every symlink in its parent resolved: the
// physical path a resource records at Create and pins at every later step. Debian 13 links /lib,
// /bin and /sbin to /usr, so /lib/x is recorded as /usr/lib/x. The entry itself is not resolved.
func ResolveCommand(p string) (string, error) {
	parent, name, err := Split(p)
	if err != nil {
		return "", err
	}
	return command("resolve", parent, name), nil
}

// ParseResolved reads what ResolveCommand printed: the physical path, or why there is none.
func ParseResolved(code int, stdout, stderr string) (string, error) {
	f, err := frame(code, stdout, stderr)
	if err != nil {
		return "", err
	}
	if f[0] != "resolved" {
		return "", fmt.Errorf("the path cannot be resolved: %s", f[2])
	}
	if _, _, err := Split(f[1]); err != nil {
		return "", fmt.Errorf("the path resolved to %q, which is not an absolute, clean path", f[1])
	}
	return f[1], nil
}

const frameFields = 7

// frame reads exactly seven NUL-terminated fields, refusing truncated output and trailing data.
func frame(code int, stdout, stderr string) ([]string, error) {
	if code == 127 {
		return nil, errors.New("perl is not installed, and nothing is removed without it: install perl-base")
	}
	if code != 0 {
		return nil, fmt.Errorf("the safeops script failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	if !strings.HasSuffix(stdout, "\x00") {
		return nil, fmt.Errorf("the safeops script printed %q, which is not a complete result", stdout)
	}
	fields := strings.Split(strings.TrimSuffix(stdout, "\x00"), "\x00")
	if len(fields) != frameFields {
		return nil, fmt.Errorf("the safeops script printed %d fields, not %d", len(fields), frameFields)
	}
	return fields, nil
}

// Parse reads what Command printed. A missing perl (exit 127) is refused rather than worked around.
func Parse(code int, stdout, stderr string) (Result, error) {
	f, err := frame(code, stdout, stderr)
	if err != nil {
		return Result{}, err
	}
	remaining, err := strconv.Atoi(f[6])
	if err != nil || remaining < 0 {
		return Result{}, fmt.Errorf("the safeops script reported %q entries remaining", f[6])
	}
	r := Result{Outcome: Outcome(f[0]), Path: f[1], Reason: f[2], Parent: f[3], Staging: f[4], ParentDevIno: f[5], Remaining: remaining}
	switch r.Outcome {
	case Absent, Removed, Restored, Kept, Partial, Refused, Installed:
		return r, nil
	}
	return Result{}, fmt.Errorf("the safeops script reported %q", f[0])
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseDigest reads what DigestCommand printed: the digest, or why there is none.
func ParseDigest(code int, stdout, stderr string) (string, error) {
	f, err := frame(code, stdout, stderr)
	if err != nil {
		return "", err
	}
	if f[0] != "digest" {
		return "", fmt.Errorf("no digest: %s", f[2])
	}
	if !hexDigest.MatchString(f[2]) {
		return "", fmt.Errorf("the safeops script printed %q, not a digest", f[2])
	}
	return f[2], nil
}

// Warning is the sentence a caller logs for a result that kept something or left something
// behind, or "" for one that needs no warning. what names the entry, as in "Symlink /var/log/journal".
func (r Result) Warning(what string) string {
	where := func() string {
		if r.Path != "" {
			return r.Path
		}
		return fmt.Sprintf("staging directory %s under the directory with dev:ino %s (once %s)", r.Staging, r.ParentDevIno, r.Parent)
	}
	switch r.Outcome {
	case Absent:
		return ""
	case Removed:
		if r.Path == "" && r.Staging == "" {
			return ""
		}
		return fmt.Sprintf("%s was removed, but %s is left behind: %s", what, where(), r.Reason)
	case Restored:
		if r.Path != "" || r.Staging != "" {
			return fmt.Sprintf("%s was kept on the machine rather than deleted: %s (left behind: %s)", what, r.Reason, where())
		}
		return fmt.Sprintf("%s was kept on the machine rather than deleted: %s", what, r.Reason)
	case Kept:
		return fmt.Sprintf("%s was kept on the machine rather than deleted, and is now in %s: %s", what, where(), r.Reason)
	case Installed:
		if r.Path == "" {
			return ""
		}
		return fmt.Sprintf("%s was installed; %s is left on the machine: %s", what, where(), r.Reason)
	case Partial:
		return fmt.Sprintf("%s was only partly removed: %d entries are left in %s, first error: %s", what, r.Remaining, where(), r.Reason)
	}
	return fmt.Sprintf("%s was kept on the machine rather than deleted: %s", what, r.Reason)
}

// InstallWarning is the sentence a caller logs for an Install result that left something on the
// machine, or "".
func (r Result) InstallWarning(what string) string {
	where := r.Path
	if where == "" {
		where = fmt.Sprintf("staging directory %s under the directory with dev:ino %s (once %s)", r.Staging, r.ParentDevIno, r.Parent)
	}
	switch {
	case r.Outcome == Installed && (r.Path != "" || r.Staging != ""):
		return fmt.Sprintf("%s was installed, and %s is left on the machine: %s", what, where, r.Reason)
	case r.Outcome == Kept:
		return fmt.Sprintf("%s was not installed, and what was there before is kept as %s: %s", what, where, r.Reason)
	}
	return ""
}

func quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}
