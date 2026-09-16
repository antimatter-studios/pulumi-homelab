/**
 * Whether a package is on the machine because somebody asked for it, or because something else did.
 *
 * apt records that difference and acts on it. A package installed as a dependency is marked **auto**
 * and `apt autoremove` is entitled to take it the moment nothing requires it any more; a package
 * somebody asked for is marked **manual** and stays. Those are two different states of the machine,
 * and only one of them matches a declaration that says "this should be installed".
 *
 * **The gap this closes is quiet.** A package already present as a dependency needs no installing, so
 * a resource that computes what is missing correctly computes nothing, installs nothing, and reports
 * success — while the machine's actual position is "present because something else wants it". Those
 * agree until the thing that wanted it changes, and then an `apt autoremove` somebody runs for
 * unrelated reasons is allowed to remove a package the stack declares. Nothing about the
 * declaration, the install, or the read was wrong; the statement was just weaker than it read.
 *
 * It lives on its own because two resources need it and a third would, and because a check written
 * per-resource is one that eventually differs per-resource — the same argument as `mode.ts`.
 *
 * **What this deliberately does not do is mark anything auto.** Taking a name out of a declaration
 * leaves the package installed and leaves its marking alone: handing a package to `autoremove` is
 * removal by a slower route, and this package does not remove what it did not install. Same
 * reasoning as `PosixAcl` never reaching for `setfacl -b`.
 */

import { shellQuote } from './ssh.ts';

/** `apt-mark showmanual` prints one name per line, and nothing for a package it does not hold. */
export function parseManual(out: string): string[] {
  return out
    .split('\n')
    .map((line) => line.trim())
    // dpkg reports a multi-arch package as `name:arch` and a declaration carries no architecture,
    // so the comparison has to happen on the bare name in both directions
    .map((name) => name.split(':')[0] ?? name)
    .filter((name) => name.length > 0 && !name.startsWith('#'));
}

/**
 * Declared packages that are installed but marked auto — the drift this exists to report.
 *
 * Not installed at all is a different answer and belongs to whatever reports missing packages: a
 * package that is absent has no marking to be wrong, and listing it here would mean two resources
 * reporting one problem in two vocabularies.
 */
export function autoMarked(
  declared: string[],
  installed: Record<string, string>,
  manual: string[],
): string[] {
  const present = Object.keys(installed).map((name) => name.split(':')[0] ?? name);
  return declared.filter((name) => present.includes(name) && !manual.includes(name));
}

/**
 * Mark these as manually installed, or nothing when there is nothing to mark.
 *
 * Null rather than a no-op command, so the caller writes nothing on a deployment where nothing
 * changed. `apt-mark manual` is idempotent and harmless to repeat, which is exactly why running it
 * unconditionally would be easy and wrong: a write on every deployment is a line in every log for a
 * machine nobody touched.
 */
export function markManualCommand(names: string[]): string | null {
  if (names.length === 0) return null;
  // sorted so the same set always produces the same command, and a reordered declaration is not a
  // different one
  const sorted = [...names].sort();
  return `apt-mark manual ${sorted.map(shellQuote).join(' ')} >/dev/null`;
}
