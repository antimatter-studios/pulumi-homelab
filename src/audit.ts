import { markedTargets } from './resources/fstab.ts';
import { ask, shellQuote, type Target, describe } from './ssh.ts';

/**
 * What is on the machine that no code mentions.
 *
 * This is the other half of partial management, and the half nothing else offers. Leaving anything
 * undeclared alone is what makes this provider safe to point at a server that already works — but
 * the price is invisible rot: a package somebody installed by hand two years ago, a unit left over
 * from a service that was replaced, an account belonging to a person who has moved on. None of it
 * appears in a diff, because a diff can only talk about resources that exist in the program.
 *
 * The trade-off is worth putting plainly: managing a machine fully means describing every resource
 * with no gaps, and anything short of that means accepting the differences in the gaps. An audit is
 * what turns *accepting* them into *knowing their size*.
 *
 * It is deliberately a function rather than a resource. It builds nothing and there is no desired
 * state for it to hold — it is a question you ask the machine, and answering it inside a
 * `pulumi up` would give the answer a lifecycle it does not want.
 */

/** What the program says should be there, so the audit can subtract it. */
/**
 * Where the audit looks.
 *
 * Every one of these is a layout a distribution chose, not a fact about Linux, so none of them is
 * hardcoded. The defaults are what Debian and its derivatives do.
 */
export interface AuditPaths {
  /** Where dpkg keeps its per-package file lists, used only for their timestamps. */
  dpkgInfo?: string;
  /** Where a machine's own systemd units live. */
  units?: string;
  fstab?: string;
  loginDefs?: string;
}

const PATHS = {
  dpkgInfo: '/var/lib/dpkg/info',
  units: '/etc/systemd/system',
  fstab: '/etc/fstab',
  loginDefs: '/etc/login.defs',
};

export interface Declared {
  /** Package names, as apt spells them. */
  packages?: string[];
  /** Unit names without the suffix, matching `SystemdUnit`'s argument: 'aiworld', not 'aiworld.service'. */
  units?: string[];
  /** Account names. */
  users?: string[];
  /** Mount points, matching `FstabEntry`'s target. */
  mounts?: string[];
}

export interface PackageFinding {
  name: string;
  /**
   * When dpkg last wrote the package's file list, as seconds since the epoch, or null where it
   * cannot be told.
   *
   * **Not an install date, and it matters that it is not.** dpkg rewrites the file list on every
   * upgrade as well as on the first install, so on any machine that has run `apt upgrade` this is
   * "last changed" — and a security update to a base package sorts above something a person chose
   * to install a year ago. Reading it as an install date leads a person to distrust the whole
   * report, which is the one outcome worth designing against.
   *
   * It is here because it is still the field that makes the package list readable rather than a
   * wall. Debian marks everything the installer put down as manually installed, so the plain
   * subtraction reports the entire base system. Timestamps cluster — the image build, the first
   * upgrade after it, each upgrade since — and the signal is a package sitting *alone* at a
   * timestamp rather than one merely near the top.
   *
   * `/var/log/apt/history.log` records real `Install:` lines and tells them from `Upgrade:`, which
   * is better meaning at much worse coverage, since it rotates. Worth adding as a second signal if
   * this one proves too blunt on a real machine, rather than in place of it.
   */
  lastWritten: number | null;
}

export interface UserFinding {
  name: string;
  uid: number;
  home: string;
  shell: string;
}

export interface AuditFindings {
  /** Manually installed packages nothing declares, most recently written by dpkg first. */
  packages: PackageFinding[];
  /** Units in `/etc/systemd/system` nothing declares. Names carry no suffix, as `SystemdUnit` takes them. */
  units: string[];
  /** Login accounts nothing declares. */
  users: UserFinding[];
  /**
   * Mount points in `/etc/fstab` that carry this provider's marker and that nothing declares.
   *
   * The one kind of stale thing no resource can find on its own. Pulumi removes what it remembers
   * creating, so an entry left behind by state that was lost or rebuilt is invisible to every
   * resource in the package — and is exactly what a marked line with no declaration means.
   */
  orphanedMounts: string[];
}

/**
 * Markers, so one round trip answers everything.
 *
 * An ssh handshake costs more than all of this work put together, and an audit that opened five
 * connections would be slow enough that nobody runs it — which is the only way for it to fail.
 * The marker is a string no line of any of these outputs can be.
 */
const MARK = '#pulumi-homelab#';
const SECTIONS = ['packages', 'times', 'units', 'passwd', 'fstab', 'logindefs'] as const;
type Section = (typeof SECTIONS)[number];

const commandFor = (paths: AuditPaths) => [
  `echo ${shellQuote(`${MARK}packages`)}`,
  // showmanual rather than the full dpkg list: a package pulled in as a dependency of something
  // declared is not rot, it is a consequence, and reporting it would bury the thing that is
  'apt-mark showmanual 2>/dev/null || true',
  `echo ${shellQuote(`${MARK}times`)}`,
  // the file list is written when the package is unpacked — on an upgrade as much as on a first
  // install, so this dates the last change and not the arrival. Names come as `pkg.list` or
  // `pkg:arch.list`.
  `stat -c '%Y %n' ${shellQuote(paths.dpkgInfo ?? PATHS.dpkgInfo)}/*.list 2>/dev/null || true`,
  `echo ${shellQuote(`${MARK}units`)}`,
  // only what is in /etc/systemd/system, which is where a person or this provider puts a unit.
  // Anything under /lib or /usr/lib arrived with a package and belongs to it, not to the machine's
  // history, so listing those would report the distribution back at you as drift.
  //
  // `-type f` because most of what is in that directory is not a unit somebody wrote: on a real Pi,
  // nine of sixteen entries were symlinks — dbus aliases like `dbus-org.bluez.service` and the links
  // `systemctl enable` leaves behind — all pointing into /lib. A symlink to a distribution unit is
  // the distribution, wearing a different name.
  `find ${shellQuote(paths.units ?? PATHS.units)} -maxdepth 1 -type f -name '*.service' 2>/dev/null || true`,
  `echo ${shellQuote(`${MARK}passwd`)}`,
  'getent passwd',
  `echo ${shellQuote(`${MARK}fstab`)}`,
  `cat ${shellQuote(paths.fstab ?? PATHS.fstab)} 2>/dev/null || true`,
  `echo ${shellQuote(`${MARK}logindefs`)}`,
  // where the distribution draws the line between accounts that came with the machine and accounts
  // somebody made. Asking rather than assuming 1000, because the answer is a policy, not a constant
  `grep -E '^UID_MIN' ${shellQuote(paths.loginDefs ?? PATHS.loginDefs)} 2>/dev/null || true`,
].join('; ');

/** Split the one reply back into the answers it contains. */
export function splitSections(out: string): Record<Section, string[]> {
  const found: Record<Section, string[]> = { packages: [], times: [], units: [], passwd: [], fstab: [], logindefs: [] };
  let current: Section | null = null;
  for (const line of out.split('\n')) {
    if (line.startsWith(MARK)) {
      const name = line.slice(MARK.length).trim() as Section;
      current = SECTIONS.includes(name) ? name : null;
      continue;
    }
    if (current !== null && line.trim().length > 0) found[current].push(line);
  }
  return found;
}

/** `1737480000 /var/lib/dpkg/info/nodejs:arm64.list` → when each package was last unpacked. */
export function parseInstallTimes(lines: string[]): Map<string, number> {
  const times = new Map<string, number>();
  for (const line of lines) {
    const space = line.indexOf(' ');
    if (space < 0) continue;
    const when = Number(line.slice(0, space));
    if (!Number.isFinite(when)) continue;
    const file = line.slice(space + 1);
    const base = file.slice(file.lastIndexOf('/') + 1).replace(/\.list$/, '');
    // `nodejs:arm64` and `nodejs` are the same package as far as anybody reading a report cares,
    // and apt-mark prints the bare name
    const name = base.split(':')[0] ?? base;
    // a multi-arch package has a list file per architecture; the later one is the one that answers
    // 'when did this last change'
    const already = times.get(name);
    if (already === undefined || when > already) times.set(name, when);
  }
  return times;
}

/** `/etc/systemd/system/aiworld.service` → `aiworld`. */
export function parseUnitNames(lines: string[]): string[] {
  return lines
    .map((line) => line.trim())
    // a directory with no units in it leaves ls with the unexpanded pattern, and a shell that
    // prints it back would otherwise have this report a service called `*`
    .filter((line) => line.endsWith('.service') && !line.includes('*'))
    .map((line) => line.slice(line.lastIndexOf('/') + 1).replace(/\.service$/, ''));
}

/**
 * The accounts a person made, out of every account on the machine.
 *
 * `nobody` is excluded by uid rather than by name: it sits at 65534, above any UID_MIN, and would
 * otherwise be reported on every machine for ever as an account nobody declared.
 */
export function parseLoginUsers(lines: string[], uidMin: number): UserFinding[] {
  const found: UserFinding[] = [];
  for (const line of lines) {
    // name:x:uid:gid:gecos:home:shell
    const fields = line.split(':');
    if (fields.length < 7) continue;
    const uid = Number(fields[2]);
    if (!Number.isFinite(uid) || uid < uidMin || uid >= 65534) continue;
    found.push({ name: fields[0] ?? '', uid, home: fields[5] ?? '', shell: fields[6] ?? '' });
  }
  return found;
}

/** `UID_MIN 1000`, or the conventional answer when the machine does not say. */
export function parseUidMin(lines: string[]): number {
  for (const line of lines) {
    const value = Number(line.replace(/^UID_MIN\s+/, '').trim());
    if (Number.isFinite(value) && value > 0) return value;
  }
  return 1000;
}

/**
 * Ask the machine what it has that the program does not mention.
 *
 * Nothing here changes anything, which is worth stating: an audit that could alter the machine
 * would be a thing people run less often, and its whole value is in being cheap enough to run
 * whenever you wonder.
 */
export async function audit(host: Target, declared: Declared = {}, paths: AuditPaths = {}): Promise<AuditFindings> {
  const asked = await ask(host, commandFor(paths));
  // every command in there ends with `|| true`, so a non-zero exit is the machine failing to answer
  // rather than an answer of 'nothing', and reporting an empty audit for it would be a lie that
  // reads exactly like a clean machine
  if (asked.code !== 0) throw new Error(`could not audit ${describe(host)}: ${asked.err.trim() || asked.out.trim()}`);

  const sections = splitSections(asked.out);
  const times = parseInstallTimes(sections.times);
  const knownPackages = new Set(declared.packages ?? []);
  const knownUnits = new Set(declared.units ?? []);
  const knownUsers = new Set(declared.users ?? []);
  const knownMounts = new Set(declared.mounts ?? []);

  const packages = sections.packages
    .map((line) => line.trim())
    .filter((name) => name.length > 0 && !knownPackages.has(name))
    .map((name) => ({ name, lastWritten: times.get(name) ?? null }))
    // most recent first, and anything undatable last rather than sorted to the top as if ancient
    .sort((a, b) => (b.lastWritten ?? -1) - (a.lastWritten ?? -1));

  return {
    packages,
    units: parseUnitNames(sections.units).filter((name) => !knownUnits.has(name)),
    users: parseLoginUsers(sections.passwd, parseUidMin(sections.logindefs))
      .filter((user) => !knownUsers.has(user.name)),
    orphanedMounts: markedTargets(sections.fstab.join('\n')).filter((target) => !knownMounts.has(target)),
  };
}
