use strict; use warnings;
use POSIX ();
use Cwd ();
use Fcntl qw(O_RDONLY O_WRONLY O_CREAT O_EXCL O_NOFOLLOW O_NONBLOCK);

# provider/safeops: remove one entry only while it is exactly what a resource made, install a file
# without replacing anybody else's, print the digest an entry would be checked against, or resolve
# a path's physical parent. Run as
#   perl -e <this> -- remove  <parent> <name> <kind> <expected>
#   perl -e <this> -- digest  <parent> <name> <kind>
#   perl -e <this> -- install <parent> <name> create|update <mode> <owner> <group>   (content on stdin)
#   perl -e <this> -- resolve <parent> <name>
# kind is symlink (expected: the exact target), file (expected: sha256 of a regular file), tree
# (expected: sha256 of the tree manifest below) or emptydir. Every decision prints one frame,
# seven NUL-terminated fields, and exits 0:
#   outcome, path, reason, parent, staging name, parent dev:ino, entries remaining
# A fault dies (exit 255). HOMELAB_SAFEOPS_TEST_HOOK, when set, is run with sh at named points so
# tests can change the filesystem mid-run; nothing sets it outside tests.

my ($mode, $parent, $name, $kind, $expected, $owner, $group) = @ARGV;
$expected //= '';
my ($pdev, $pino, $priv, $cleanup) = ('', '', '', '');
my $prefix = $parent eq '/' ? '' : $parent;

sub hook {
  my ($point) = @_;
  my $h = $ENV{HOMELAB_SAFEOPS_TEST_HOOK};
  system('sh', '-c', $h, 'hook', $point) if defined $h && $h ne '';
}

sub frame { print join("\0", @_), "\0"; exit 0; }

# done reports an outcome. rel, when set, is what is left behind, relative to the pinned parent; its
# absolute path is reported only while the parent is still the directory that was pinned, and
# otherwise the staging name and the parent's dev:ino say where it is.
sub done {
  my ($outcome, $rel, $reason, $remaining) = @_;
  if ($cleanup ne '' && lstat($cleanup)) {
    unlink($cleanup) or $reason .= " (the staged copy $cleanup could not be removed: $!)";
  }
  my $path = '';
  if ($rel ne '') {
    my $cwd = Cwd::getcwd();
    my @p = lstat($parent);
    if (defined $cwd && $cwd eq $parent && @p && "$p[0]" eq "$pdev" && "$p[1]" eq "$pino") {
      $path = "$prefix/$rel";
    } else {
      $reason .= " (the parent moved: it is in staging directory $priv under the directory with dev:ino $pdev:$pino)";
    }
  }
  frame($outcome, $path, $reason, $parent, $priv, $pdev eq '' ? '' : "$pdev:$pino", $remaining // 0);
}

# need_sha256sum refuses, with nothing touched, a machine without coreutils' sha256sum: every digest
# is sha256sum reading a descriptor this script opened (perl-base has no Digest::SHA). No fallback.
sub need_sha256sum {
  for my $d (split /:/, ($ENV{PATH} // '/usr/bin:/bin')) { return if $d ne '' && -x "$d/sha256sum"; }
  done('refused', '', 'sha256sum (coreutils) is not installed, so no digest can be computed: nothing was touched');
}

sub random_name {
  open(my $ur, '<', '/dev/urandom') or done('refused', '', "no randomness for a private name: $!");
  my $rb = '';
  (read($ur, $rb, 16) // 0) == 16 or done('refused', '', 'no randomness for a private name');
  close $ur;
  return '.homelab-safeops.' . unpack('H*', $rb);
}

sub renameat2_nr {
  my $arch = (POSIX::uname())[4];
  my %nr = (x86_64 => 316, aarch64 => 276, i686 => 353, i386 => 353);
  return ($nr{$arch} // ($arch =~ /^arm/ ? 382 : undef), $arch);
}

# renameat2(AT_FDCWD, from, AT_FDCWD, to, RENAME_NOREPLACE): never replaces anything, an empty
# directory included, and never copies. Returns '' or the error.
my $rename_enoent = 0;
sub rename_noreplace {
  my ($from, $to) = @_;
  $rename_enoent = 0;
  my ($nr, $arch) = renameat2_nr();
  return "there is no rename without replacement on $arch" unless defined $nr;
  my ($f, $t) = ($from, $to);
  return '' if syscall($nr, -100, $f, -100, $t, 1) == 0;
  $rename_enoent = $!{ENOENT} ? 1 : 0;
  return "$!";
}

# sha_exec runs sha256sum with stdin set up by $child (in the child) and fed by $parent_side (in
# the parent), and returns the hex digest or (undef, why). Empty, malformed or failed output is a
# failure, never a digest.
sub sha_exec {
  my ($child, $parent_side) = @_;
  pipe(my $r, my $w) or return (undef, "a pipe cannot be made: $!");
  my $pid = fork;
  return (undef, "sha256sum cannot be started: $!") unless defined $pid;
  if ($pid == 0) {
    close $r;
    open(STDOUT, '>&', $w) or POSIX::_exit(126);
    $child->();
    exec('sha256sum') or POSIX::_exit(127);
  }
  close $w;
  $parent_side->() if $parent_side;
  my $out = do { local $/; <$r> } // '';
  close $r;
  waitpid($pid, 0);
  return (undef, "the digest could not be computed (sha256sum exit " . ($? >> 8) . ")") if $? != 0;
  return (undef, 'the digest came back empty') if $out eq '';
  return (undef, 'the digest came back malformed') unless $out =~ /\A([0-9a-f]{64}) /;
  return ($1, '');
}

# file_sha is the sha256 of a regular file, opened without following a link and checked by fstat to
# be the entry lstat saw.
sub file_sha {
  my ($path, $dev, $ino) = @_;
  sysopen(my $fh, $path, O_RDONLY | O_NOFOLLOW | O_NONBLOCK) or return (undef, "it cannot be opened without following links: $!");
  my @st = stat($fh);
  return (undef, 'it is not a regular file') unless @st && -f _;
  return (undef, 'it changed while it was opened') if defined $dev && ("$st[0]" ne "$dev" || "$st[1]" ne "$ino");
  return sha_exec(sub { open(STDIN, '<&', $fh) or POSIX::_exit(125); });
}

sub string_sha {
  my ($s) = @_;
  pipe(my $ir, my $iw) or return (undef, "a pipe cannot be made: $!");
  return sha_exec(sub { close $iw; open(STDIN, '<&', $ir) or POSIX::_exit(125); },
                  sub { close $ir; print {$iw} $s; close $iw; });
}

# manifest walks a tree with lstat: it never follows a symlink (the link's target string is
# recorded), never crosses to another st_dev, and records for each entry, sorted by name bytes:
#   type NUL path NUL permission bits in octal NUL payload NUL
# where the payload is a file's sha256, a link's target, or nothing for a directory.
sub manifest {
  my ($path, $rel, $dev, $out) = @_;
  my @st = lstat($path);
  return "$rel cannot be examined: $!" unless @st;
  return "$rel is on another filesystem" if "$st[0]" ne "$dev";
  my $perm = sprintf('%o', $st[2] & 07777);
  if (-l _) {
    my $t = readlink($path);
    return "the target of $rel cannot be read: $!" unless defined $t;
    $$out .= "l\0$rel\0$perm\0$t\0";
  } elsif (-f _) {
    my ($d, $e) = file_sha($path, $st[0], $st[1]);
    return "$rel: $e" unless defined $d;
    $$out .= "f\0$rel\0$perm\0$d\0";
  } elsif (-d _) {
    $$out .= "d\0$rel\0$perm\0\0";
    opendir(my $dh, $path) or return "$rel cannot be read: $!";
    my @names = sort grep { $_ ne '.' && $_ ne '..' } readdir($dh);
    closedir($dh);
    for my $n (@names) {
      my $e = manifest("$path/$n", $rel eq '.' ? $n : "$rel/$n", $dev, $out);
      return $e if $e ne '';
    }
  } else {
    return "$rel is not a file, a directory or a symlink";
  }
  return '';
}

sub tree_sha {
  my ($path) = @_;
  my @st = lstat($path);
  return (undef, "it cannot be examined: $!") unless @st;
  return (undef, 'it is not a directory') if -l _ || !-d _;
  my $m = '';
  my $e = manifest($path, '.', $st[0], \$m);
  return (undef, $e) if $e ne '';
  return string_sha($m);
}

sub digest_of {
  my ($path, $kind) = @_;
  if ($kind eq 'file') {
    my @st = lstat($path);
    return (undef, "it cannot be examined: $!") unless @st;
    return (undef, 'it is not a regular file') if -l _ || !-f _;
    return file_sha($path, $st[0], $st[1]);
  }
  return tree_sha($path) if $kind eq 'tree';
  return (undef, "there is no digest for kind $kind");
}

# mounts_under names a mount point at or under path, or ''. st_dev alone misses a bind mount of the
# same filesystem, so the mount table is read.
sub mounts_under {
  my ($path) = @_;
  open(my $mi, '<', '/proc/self/mountinfo') or return "the mount table cannot be read: $!";
  while (my $line = <$mi>) {
    my @f = split / /, $line;
    next unless @f > 4;
    my $mp = $f[4];
    $mp =~ s/\\([0-7]{3})/chr(oct($1))/ge;
    return "$mp is a mount point" if $mp eq $path || index($mp, "$path/") == 0;
  }
  return '';
}

# remove_tree removes a captured tree depth first with lstat, unlink and rmdir, never entering
# another st_dev. Returns the number of entries left and the first error.
sub remove_tree {
  my ($path, $dev) = @_;
  my @st = lstat($path);
  return (0, '') if !@st && $!{ENOENT};
  return (1, "$path cannot be examined: $!") unless @st;
  if (-d _ && !-l _) {
    return (1, "$path is on another filesystem, so it was not entered") if "$st[0]" ne "$dev";
    opendir(my $dh, $path) or return (1, "$path cannot be read: $!");
    my @names = grep { $_ ne '.' && $_ ne '..' } readdir($dh);
    closedir($dh);
    my ($left, $first) = (0, '');
    for my $n (@names) {
      my ($l, $e) = remove_tree("$path/$n", $dev);
      $left += $l;
      $first = $e if $first eq '';
    }
    return ($left + 1, $first) if $left > 0;
    rmdir($path) or return (1, "$path cannot be removed: $!");
    return (0, '');
  }
  unlink($path) or return (1, "$path cannot be removed: $!");
  return (0, '');
}

# resolve: the physical path the entry would have, every symlink in its parent resolved, for a
# resource to record at Create and pin at every later step (Debian links /lib, /bin and /sbin to
# /usr, so a path under them would otherwise be refused for its parent).
if ($mode eq 'resolve') {
  my $abs = Cwd::abs_path($parent);
  done('refused', '', "the parent $parent cannot be resolved: $!") unless defined $abs;
  my @st = lstat($abs);
  done('refused', '', "the parent $parent resolves to $abs, which is not a directory") if !@st || -l _ || !-d _;
  frame('resolved', ($abs eq '/' ? '' : $abs) . "/$name", '', $parent, '', "$st[0]:$st[1]", 0);
}

# Pin the parent once: a real directory, entered, the same dev:ino, at the recorded physical path.
my @pl = lstat($parent);
if (!@pl) {
  done('absent', '', '') if $!{ENOENT} && $mode eq 'remove';
  done('refused', '', "the parent $parent cannot be examined: $!");
}
done('refused', '', "the parent $parent is a symlink, not the recorded directory") if -l _;
done('refused', '', "the parent $parent is not a directory") unless -d _;
chdir($parent) or done('refused', '', "the parent $parent cannot be entered: $!");
my @here = lstat('.');
done('refused', '', "the parent $parent changed while it was entered") if !@here || "$here[0]" ne "$pl[0]" || "$here[1]" ne "$pl[1]";
($pdev, $pino) = @here[0, 1];
my $cwd = Cwd::getcwd();
done('refused', '', "the parent $parent resolves to " . ($cwd // 'nothing')) if !defined($cwd) || $cwd ne $parent;

if ($mode eq 'digest') {
  need_sha256sum();
  my @nl = lstat($name);
  done('refused', '', "it cannot be examined: $!") unless @nl;
  my $m = mounts_under("$prefix/$name");
  done('refused', '', $m) if $m ne '';
  my ($d, $e) = digest_of($name, $kind);
  done('refused', '', $e) unless defined $d;
  frame('digest', '', $d, $parent, '', "$pdev:$pino", 0);
}
done('refused', '', "unknown mode $mode") unless $mode eq 'remove' || $mode eq 'install';

if (($here[2] & 022) && !($here[2] & 01000)) {
  done('refused', '', "the parent $parent is writable by group or others without the sticky bit, so no name in it can be trusted");
}

# install: stage the content from stdin beside the name (O_CREAT|O_EXCL|O_NOFOLLOW, 0600, owner
# then mode, since chown after chmod would clear set-ID bits), then
#   update: rename(2) over the name: replacing the declared file is the intended act;
#   create: move anything at the name aside with renameat2(RENAME_NOREPLACE) to a fresh name, then
#           link(2) the staged file in, which fails rather than replace something that appeared
#           meanwhile. What was moved aside is removed only when it is a regular file holding
#           exactly the new content; otherwise it is kept and reported by path.
# A directory at the name is refused. The staged copy is removed whatever happens.
if ($mode eq 'install') {
  my ($how, $fmode) = ($kind, $expected);
  done('refused', '', "install takes create or update, not $how") unless $how eq 'create' || $how eq 'update';
  done('refused', '', "$fmode is not an octal mode") unless $fmode =~ /\A[0-7]{3,4}\z/;
  my $uid = ($owner // '') =~ /\A\d+\z/ ? $owner : getpwnam($owner // '');
  my $gid = ($group // '') =~ /\A\d+\z/ ? $group : getgrnam($group // '');
  done('refused', '', "there is no user " . ($owner // '')) unless defined $uid;
  done('refused', '', "there is no group " . ($group // '')) unless defined $gid;
  need_sha256sum() if $how eq 'create';
  my @nl = lstat($name);
  done('refused', '', "it cannot be examined: $!") if !@nl && !$!{ENOENT};
  done('refused', '', "$prefix/$name is a directory; refusing to replace it with a file") if @nl && -d _ && !-l _;
  my $mounted = mounts_under("$prefix/$name");
  done('refused', '', "$mounted, so nothing was written") if $mounted ne '';

  my $staged = random_name();
  sysopen(my $out, $staged, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600) or done('refused', '', "the content cannot be staged beside it: $!");
  $cleanup = $staged;
  binmode STDIN;
  binmode $out;
  while (1) {
    my $n = sysread(STDIN, my $buf, 65536);
    done('refused', '', "the content could not be read: $!") unless defined $n;
    last if $n == 0;
    my $off = 0;
    while ($off < $n) {
      my $w = syswrite($out, $buf, $n - $off, $off);
      done('refused', '', "the content could not be staged: $!") unless defined $w;
      $off += $w;
    }
  }
  chown($uid, $gid, $out) or done('refused', '', "the staged copy cannot be given its owner: $!");
  chmod(oct($fmode), $out) or done('refused', '', "the staged copy cannot be given its mode: $!");
  close($out) or done('refused', '', "the content could not be staged: $!");

  if ($how eq 'update') {
    rename($staged, $name) or done('refused', '', "it could not be installed: $!");
    $cleanup = '';
    done('installed', '', '');
  }
  my ($new) = file_sha($staged);
  done('refused', '', 'the staged content could not be digested') unless defined $new;
  my $aside = '';
  if (@nl) {
    $aside = random_name() . '.kept';
    my $e = rename_noreplace($name, $aside);
    done('refused', '', "what is there could not be moved aside, so nothing was written: $e") if $e ne '';
  }
  hook('aside');
  if (!link($staged, $name)) {
    my $e = "$!";
    done($aside ne '' ? 'kept' : 'refused', $aside, "something appeared at it while it was written, and was not replaced: $e");
  }
  unlink($staged) or done('installed', $staged, "it is installed, but the staged copy could not be removed: $!");
  $cleanup = '';
  if ($aside ne '') {
    my @al = lstat($aside);
    if (@al && -f _ && !-l _) {
      my ($d) = file_sha($aside, $al[0], $al[1]);
      done('installed', '', '') if defined $d && $d eq $new && unlink($aside);
    }
    done('installed', $aside, 'what was there is not what this resource writes, so it was moved aside and kept');
  }
  done('installed', '', '');
}
my $valid = $kind eq 'symlink' ? $expected ne '' : $kind eq 'emptydir' ? 1 : $kind eq 'file' || $kind eq 'tree' ? $expected =~ /\A[0-9a-f]{64}\z/ : 0;
done('refused', '', "the expected " . ($kind eq 'symlink' ? 'target' : 'digest') . " is empty or malformed, so nothing can be proven this resource's") unless $valid;

my @nl = lstat($name);
if (!@nl) {
  done('absent', '', '') if $!{ENOENT};
  done('refused', '', "it cannot be examined: $!");
}
my $mounted = mounts_under("$prefix/$name");
done('refused', '', "$mounted, so nothing was touched") if $mounted ne '';
need_sha256sum() if $kind eq 'file' || $kind eq 'tree';
hook('pinned');

# A private directory beside it, with an unpredictable name, checked right after mkdir.
my $try = random_name();
mkdir($try, 0700) or done('refused', '', "a private directory cannot be made beside it: $!");
$priv = $try;
my @sl = lstat($priv);
done('refused', $priv, 'the private directory is not the one just made') if !@sl || -l _ || !-d _ || $sl[4] != $> || ($sl[2] & 077);

# Capture without replacing, and check the private directory is still the one made.
my $cap = "$priv/entry";
my $err = rename_noreplace($name, $cap);
if ($err ne '') {
  my $gone = $rename_enoent;
  rmdir($priv) or done('refused', $priv, "it could not be captured ($err), and the private directory could not be removed: $!");
  $priv = '';
  done('absent', '', '') if $gone;
  done('refused', '', "it could not be captured, so it was not touched: $err");
}
my @sl2 = lstat($priv);
done('kept', $cap, 'the private directory changed while the entry was captured') if !@sl2 || "$sl2[0]" ne "$sl[0]" || "$sl2[1]" ne "$sl[1]";
hook('captured');

# Verify the captured entry only.
my @cl = lstat($cap);
my ($match, $why) = (0, '');
if (!@cl) {
  $why = "the captured entry cannot be examined: $!";
} elsif ($kind eq 'symlink') {
  if (!-l _) { $why = 'it is not a symlink'; }
  else {
    my $t = readlink($cap);
    if (!defined $t) { $why = "its target cannot be read: $!"; }
    elsif ($t ne $expected) { $why = 'it points somewhere else'; }
    else { $match = 1; }
  }
} elsif ($kind eq 'file' || $kind eq 'tree') {
  my ($d, $e) = digest_of($cap, $kind);
  if (!defined $d) { $why = $e; }
  elsif ($d ne $expected) { $why = 'its content is not what was written'; }
  else { $match = 1; }
} elsif ($kind eq 'emptydir') {
  if (-l _ || !-d _) { $why = 'it is not a directory'; }
  else { $match = 1; }
}
hook('verified');

if ($match && $kind eq 'emptydir') {
  if (!rmdir($cap)) { $match = 0; $why = ($!{ENOTEMPTY} || $!{EEXIST}) ? 'it is not empty' : "it could not be removed: $!"; }
} elsif ($match) {
  my ($left, $first) = remove_tree($cap, $cl[0]);
  done('partial', $priv, "removal stopped: $first", $left) if $left > 0;
}
if ($match) {
  rmdir($priv) or done('removed', $priv, "the private directory could not be removed: $!");
  $priv = '';
  done('removed', '', '');
}

# Not this resource's: put it back without replacing anything.
if (-d $cap && !-l $cap) {
  my $e = rename_noreplace($cap, $name);
  done('kept', $cap, "$why, and it could not be put back: $e") if $e ne '';
} else {
  link($cap, $name) or done('kept', $cap, "$why, and it could not be put back: $!");
  unlink($cap) or done('restored', $cap, "$why; it is back, and also still at the captured path: $!");
}
rmdir($priv) or done('restored', $priv, "$why; it is back, but the private directory could not be removed: $!");
$priv = '';
done('restored', '', $why);
