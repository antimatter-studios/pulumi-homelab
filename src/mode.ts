/**
 * Putting a file mode into the shape the code writes it in.
 *
 * `stat` says `644` where every piece of documentation, and everybody's code, says `0644`. Compared
 * as strings those differ, so a resource that stores what `stat` said and diffs it against what the
 * program declared reports drift on every refresh for ever, on a file nobody has touched. That is
 * the loudest possible way to make drift detection worthless: a report that always says something
 * is wrong teaches people to stop reading it.
 *
 * It lives on its own because the same three lines were about to exist in a third resource, and the
 * bug this prevents has already happened once. A normalisation that is written per-resource is one
 * that eventually differs per-resource, and then two resources disagree about the same directory.
 */
export function normaliseMode(raw: string): string {
  const mode = raw.trim();
  // three digits is stat's ordinary answer; four already carries the leading zero, or a setuid,
  // setgid or sticky bit that is not a leading zero at all and must survive untouched
  return mode.length === 3 ? `0${mode}` : mode;
}

/** The three bits the high digit of a mode carries, which are not permissions at all. */
export interface SpecialBits {
  /**
   * Run as the file's owner.
   *
   * Real on an executable and **meaningless on a directory on Linux**, where `S_ISUID` has no
   * defined behaviour at all. It is read and preserved here because a mode string can carry it and
   * a resource adopting such a path has to describe what is actually there — not because setting it
   * on a directory does anything.
   */
  setuid: boolean;
  /**
   * On a directory: new entries inherit the directory's group rather than their creator's.
   *
   * What makes a shared area shared. Without it, a file somebody writes into a group directory
   * belongs to that person's own group and nobody else in the team can touch it.
   */
  setgid: boolean;
  /**
   * Only an entry's owner may remove it.
   *
   * **The bit that pairs with a write grant.** Write permission on a directory is what permits
   * deleting the entries *in* it — so an account granted write on a shared directory can remove
   * other people's work, including directories it cannot read into. The sticky bit is what makes a
   * shared writable directory safe, and it is why `/tmp` has had it since before any of this.
   */
  sticky: boolean;
}

const NONE: SpecialBits = { setuid: false, setgid: false, sticky: false };

/** Whether a mode is written the way a mode is. */
export function modeRefusal(mode: string): string | null {
  if (/^[0-7]{3,4}$/.test(mode.trim())) return null;
  return `${mode} is not a mode: write three or four octal digits, as in '0755' or '2775'`;
}

/** What the high digit of a mode says. */
export function specialBitsOf(mode: string): SpecialBits {
  const digit = Number(normaliseMode(mode)[0] ?? '0');
  return { setuid: (digit & 4) !== 0, setgid: (digit & 2) !== 0, sticky: (digit & 1) !== 0 };
}

/**
 * Why a mode and the flags beside it cannot both be right, or null when they can.
 *
 * A mode carrying a non-zero high digit has already said what the special bits are, and a flag
 * beside it is a second answer to the same question. Rather than work out a precedence nobody would
 * remember, both spellings are accepted and mixing them is refused: `mode: '2775'` **or**
 * `mode: '0775', setgid: true`. A leading `0` is not a statement — `0755` is how everybody writes
 * plain `755` — so flags beside it simply fill the digit in.
 */
export function specialBitsRefusal(mode: string, flags: Partial<SpecialBits>): string | null {
  const given = (['setuid', 'setgid', 'sticky'] as const).filter((bit) => flags[bit] !== undefined);
  if (given.length === 0) return null;
  const stated = specialBitsOf(mode);
  if (!stated.setuid && !stated.setgid && !stated.sticky) return null;
  return `mode '${mode}' already sets the special bits in its leading digit, and ${given.join(', ')} `
    + `${given.length === 1 ? 'says' : 'say'} the same thing again. Write one or the other: `
    + `'${mode}', or '0${normaliseMode(mode).slice(1)}' with the flags.`;
}

/**
 * A mode with its special bits filled in from the flags beside it.
 *
 * Four digits always, so what is stored and what `stat -c %a` reports on a directory with any of
 * these bits set are the same string.
 */
export function withSpecialBits(mode: string, flags: Partial<SpecialBits>): string {
  const normalised = normaliseMode(mode);
  const stated = specialBitsOf(normalised);
  const bits = { ...NONE };
  for (const bit of ['setuid', 'setgid', 'sticky'] as const) {
    // an undefined flag is one nobody wrote, and must not overwrite what the mode already said
    bits[bit] = flags[bit] ?? stated[bit];
  }
  const digit = (bits.setuid ? 4 : 0) | (bits.setgid ? 2 : 0) | (bits.sticky ? 1 : 0);
  return `${digit}${normalised.slice(1)}`;
}
