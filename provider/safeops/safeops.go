// Package safeops removes one entry from a machine only when it is still exactly what a resource
// made, and never anything else, even something swapped in while the removal runs.
//
// The work is done by a perl script run over the remote.Runner, so that each step is one exact
// system call rather than a shell utility whose behaviour varies by version: coreutils mv, asked for
// no-clobber or no-copy, has been seen to fall back to a plain rename. perl-base is Essential on
// Debian, so the script needs nothing installed; where perl is missing the removal is refused, with
// no fallback.
//
// The script:
//
//  1. pins the parent once: lstat says it is a real directory (not a symlink), chdir enters it, the
//     directory entered has the same device and inode, and its physical path is the recorded
//     parent. From then on only relative names are used.
//  2. captures the entry with rename(2) into a fresh private directory beside it (mkdir 0700).
//     rename never copies, so an entry on another filesystem (a mount point) fails rather than
//     being copied.
//  3. verifies the captured entry only, where nothing else can swap it: its kind, a symlink's target
//     compared exactly by readlink, and optionally a digest printed by a command the caller supplies.
//  4. on a match removes it; on a mismatch restores it without clobbering, with link(2) and then
//     unlink(2) for anything but a directory (link fails with EEXIST rather than replace, and does
//     not follow a symlink), and renameat2(AT_FDCWD, …, AT_FDCWD, …, RENAME_NOREPLACE) through perl's
//     syscall for a directory, since link(2) cannot link one and rename(2) would replace an empty
//     directory at the name (SYS_renameat2 is 316 on x86_64, 276 on aarch64, 382 on arm, 353 on
//     i386). ENOSYS, EINVAL, EEXIST or an unknown architecture leaves the entry in the private
//     directory; there is no fallback. What cannot be put back has its exact path reported.
//
// Every outcome is reported as a Result, never as a failure: the caller turns kept and refused into
// warnings, so a delete that finds something unexpected keeps it, says so, and succeeds.
package safeops

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Kind is what the entry must be for it to count as the resource's.
type Kind string

const (
	// Symlink is a symbolic link whose target is exactly Expect.Target.
	Symlink Kind = "symlink"
	// File is a regular file (not a symlink to one).
	File Kind = "file"
	// Dir is a directory (not a symlink to one), removed with everything in it.
	Dir Kind = "dir"
	// EmptyDir is a directory (not a symlink to one) removed with rmdir(2), so only while it is
	// empty; one with anything in it is put back.
	EmptyDir Kind = "emptydir"
)

// Expect is what the entry must be.
type Expect struct {
	Kind Kind
	// Target is the exact symlink target for Kind Symlink, compared byte for byte (a trailing
	// newline included).
	Target string
	// DigestCommand, when set, is a shell command run with the captured entry's path as $1; its
	// output with trailing whitespace removed must equal Digest. It runs inside the pinned parent,
	// on the captured entry, which nothing else can reach.
	DigestCommand string
	Digest        string
}

// Outcome is what happened.
type Outcome string

const (
	// Absent: there was nothing at the path (or no parent), so nothing was done.
	Absent Outcome = "absent"
	// Removed: the entry was the resource's and is gone. Result.Path is set when the private
	// directory could not be removed afterwards.
	Removed Outcome = "removed"
	// Restored: the entry was not the resource's and is back at its path, untouched.
	Restored Outcome = "restored"
	// Kept: the entry was captured and is not back at its path; Result.Path is where it is.
	Kept Outcome = "kept"
	// Refused: nothing was touched, for the reason given (a parent that is not the recorded one,
	// an entry that could not be captured).
	Refused Outcome = "refused"
)

// Result is the outcome, any path left behind on the machine, and why.
type Result struct {
	Outcome Outcome
	// Path is where an entry was kept, or the private directory left behind; "" when nothing was.
	Path   string
	Reason string
}

// Split checks path is an absolute, clean path to an entry and returns its parent and name.
func Split(p string) (parent, name string, err error) {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" {
		return "", "", fmt.Errorf("%q is not an absolute, clean path to an entry", p)
	}
	return path.Dir(p), path.Base(p), nil
}

// Command is the shell command that removes the entry at p when it is as expected. It runs as the
// user that may remove it; the caller escalates it.
func Command(p string, want Expect) (string, error) {
	parent, name, err := Split(p)
	if err != nil {
		return "", err
	}
	switch want.Kind {
	case Symlink, File, Dir, EmptyDir:
	default:
		return "", fmt.Errorf("unknown kind %q", want.Kind)
	}
	args := []string{"perl", "-e", quote(script), "--", quote(parent), quote(name), quote(string(want.Kind)),
		quote(want.Target), quote(want.DigestCommand), quote(want.Digest)}
	return strings.Join(args, " "), nil
}

// Parse reads what Command printed. code 127 is a machine without perl, which is refused rather
// than worked around.
func Parse(code int, stdout, stderr string) (Result, error) {
	if code == 127 {
		return Result{}, errors.New("perl is not installed, and nothing is removed without it: install perl-base")
	}
	if code != 0 {
		return Result{}, fmt.Errorf("the removal script failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	fields := strings.Split(stdout, "\x00")
	if len(fields) < 3 {
		return Result{}, fmt.Errorf("the removal script printed %q, not a result", stdout)
	}
	r := Result{Outcome: Outcome(fields[0]), Path: fields[1], Reason: fields[2]}
	switch r.Outcome {
	case Absent, Removed, Restored, Kept, Refused:
		return r, nil
	}
	return Result{}, fmt.Errorf("the removal script reported %q", fields[0])
}

// Warning is the sentence a caller logs for a result that kept something or left something behind,
// or "" for one that needs no warning. what names the entry, as in "Symlink /var/log/journal".
func (r Result) Warning(what string) string {
	switch r.Outcome {
	case Absent:
		return ""
	case Removed:
		if r.Path == "" {
			return ""
		}
		return fmt.Sprintf("%s was removed, but %s is left behind: %s", what, r.Path, r.Reason)
	case Restored:
		return fmt.Sprintf("%s was kept on the machine rather than deleted: %s", what, r.Reason)
	case Kept:
		return fmt.Sprintf("%s was kept on the machine rather than deleted, and is now at %s: %s", what, r.Path, r.Reason)
	}
	where := ""
	if r.Path != "" {
		where = "; " + r.Path + " is left behind"
	}
	return fmt.Sprintf("%s was kept on the machine rather than deleted: %s%s", what, r.Reason, where)
}

func quote(text string) string {
	return "'" + strings.ReplaceAll(text, "'", `'\''`) + "'"
}

// script is the perl that does the work. Arguments: parent, name, kind, target, digest command,
// digest. It prints outcome NUL path NUL reason NUL and exits 0 for every decision; it dies (exit
// 255) only on a fault.
const script = `use strict; use warnings; use POSIX (); use Cwd ();
my ($parent, $name, $kind, $target, $dcmd, $digest) = @ARGV;
sub done { my ($o, $p, $r) = @_; print join("\0", $o, $p, $r), "\0"; exit 0; }
my $prefix = $parent eq "/" ? "" : $parent;
my @pl = lstat($parent);
if (!@pl) { done("absent", "", "") if $!{ENOENT}; done("refused", "", "the parent $parent cannot be examined: $!"); }
done("refused", "", "the parent $parent is a symlink, not the recorded directory") if -l _;
done("refused", "", "the parent $parent is not a directory") unless -d _;
chdir($parent) or done("refused", "", "the parent $parent cannot be entered: $!");
my @here = lstat(".");
done("refused", "", "the parent $parent changed while it was entered") if !@here || $here[0] != $pl[0] || $here[1] != $pl[1];
my $cwd = Cwd::getcwd();
done("refused", "", "the parent $parent resolves to " . ($cwd // "nothing")) if !defined($cwd) || $cwd ne $parent;
my @nl = lstat($name);
if (!@nl) { done("absent", "", "") if $!{ENOENT}; done("refused", "", "it cannot be examined: $!"); }
my $priv;
for (1 .. 20) {
  my $try = sprintf(".homelab-safeops.%d.%08x", $$, int(rand(4294967295)));
  if (mkdir($try, 0700)) { $priv = $try; last; }
  done("refused", "", "a private directory cannot be made beside it: $!") unless $!{EEXIST};
}
done("refused", "", "no private directory name was free") unless defined $priv;
my $cap = "$priv/entry";
my $kept = "$prefix/$cap";
my $left = "$prefix/$priv";
if (!rename($name, $cap)) {
  my ($err, $gone) = ("$!", $!{ENOENT});
  rmdir($priv) or done("refused", $left, "it could not be captured ($err), and the private directory could not be removed: $!");
  done("absent", "", "") if $gone;
  done("refused", "", "it could not be captured, so it was not touched: $err");
}
my @cl = lstat($cap);
my $match = 0;
my $why = "";
if (!@cl) { $why = "the captured entry cannot be examined: $!"; }
elsif ($kind eq "symlink") {
  if (!-l _) { $why = "it is not a symlink"; }
  else { my $t = readlink($cap); if (!defined $t) { $why = "its target cannot be read: $!"; } elsif ($t ne $target) { $why = "it points somewhere else"; } else { $match = 1; } }
}
elsif ($kind eq "file") { if (-l _ || !-f _) { $why = "it is not a regular file"; } else { $match = 1; } }
elsif ($kind eq "dir" || $kind eq "emptydir") { if (-l _ || !-d _) { $why = "it is not a directory"; } else { $match = 1; } }
if ($match && $dcmd ne "") {
  my $got = "";
  if (open(my $fh, "-|", "sh", "-c", $dcmd, "sh", $cap)) { local $/; $got = <$fh> // ""; close($fh); }
  my $status = $?;
  $got =~ s/\s+\z//;
  if ($status != 0) { $match = 0; $why = "its digest could not be computed"; }
  elsif ($got ne $digest) { $match = 0; $why = "its content is not what was written"; }
}
if ($match && $kind eq "emptydir" && !rmdir($cap)) { $match = 0; $why = $!{ENOTEMPTY} || $!{EEXIST} ? "it is not empty" : "it could not be removed: $!"; }
elsif ($match && $kind eq "emptydir") { rmdir($priv) or done("removed", $left, "the private directory could not be removed: $!"); done("removed", "", ""); }
if ($match) {
  my $isdir = !-l $cap && -d _;
  my $ok = $isdir ? system("rm", "-rf", "--", $cap) == 0 : unlink($cap);
  done("kept", $kept, "it matched, but could not be removed: $!") if !$ok || -e $cap || -l $cap;
  rmdir($priv) or done("removed", $left, "the private directory could not be removed: $!");
  done("removed", "", "");
}
if (-d $cap && !-l $cap) {
  my %renameat2 = (x86_64 => 316, aarch64 => 276, i686 => 353, i386 => 353);
  my $arch = (POSIX::uname())[4];
  my $nr = $renameat2{$arch} // ($arch =~ /^arm/ ? 382 : undef);
  done("kept", $kept, "$why, and a directory cannot be put back without replacing on $arch") unless defined $nr;
  my ($from, $to) = ($cap, $name);
  # renameat2(AT_FDCWD, from, AT_FDCWD, to, RENAME_NOREPLACE): never replaces, not even an empty
  # directory, which rename(2) would. ENOSYS or EINVAL (no renameat2, or a filesystem without
  # RENAME_NOREPLACE) keeps the tree where it is, reported; there is no fallback.
  syscall($nr, -100, $from, -100, $to, 1) == 0 or done("kept", $kept, "$why, and it could not be put back: $!");
} else {
  link($cap, $name) or done("kept", $kept, "$why, and it could not be put back: $!");
  unlink($cap) or done("restored", $kept, "$why; it is back, and also still at the captured path: $!");
}
rmdir($priv) or done("restored", $left, "$why; it is back, but the private directory could not be removed: $!");
done("restored", "", $why);
`
