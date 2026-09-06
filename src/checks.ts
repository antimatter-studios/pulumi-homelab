import { shellQuote } from './ssh.ts';

/**
 * Questions to hand to a `Precondition`.
 *
 * They live together because they are all the same kind of thing — a shell command that exits zero
 * when the machine is in a state the code requires — and because a question asked two ways in two
 * repositories is a question with two answers.
 */

/**
 * Whether a path is a real mount point rather than an empty directory that looks exactly like one.
 *
 * This is one of the quietest failures a machine can have. A deployment onto a mount point whose
 * disk has not come up does not fail: the directory exists, it is writable, and everything written
 * to it lands on the root filesystem. Nothing reports an error at any point. The disk then mounts
 * on the next reboot, hides the data that was written underneath it, and the service comes back
 * with an empty world — or, worse, builds a second one over the top of a perfectly good one.
 *
 * systemd's `RequiresMountsFor` protects the boot and does nothing for the deployment, which runs
 * over ssh long after the machine is up.
 *
 * `mountpoint` is the tool for exactly this and is in util-linux, so it is on any machine this
 * provider would be pointed at; `findmnt` is the fallback for a stripped image that has one and not
 * the other.
 */
export function mountedAt(path: string): string {
  const quoted = shellQuote(path);
  return `mountpoint -q ${quoted} 2>/dev/null || findmnt -rno TARGET ${quoted} >/dev/null 2>&1`;
}
