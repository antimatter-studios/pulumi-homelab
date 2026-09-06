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
