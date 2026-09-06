import type * as pulumi from '@pulumi/pulumi';

/**
 * Declared, effective, and where the two disagree.
 *
 * This is the central argument of the package, and it was implemented five times before it was
 * named. `BootConfig` asks `vcgencmd`, `Journald` asks `systemd-analyze cat-config`, `SambaShare`
 * asks `testparm`, `SshdConfig` asks `sshd -T`, and each invented its own field names for the same
 * idea: **what this resource wrote is not what the system does.**
 *
 * The distinction matters because in every one of those cases something else can win. A drop-in
 * that sorts later, a `config.txt` section that never matches the board, a `Match` block, a
 * distribution default — none of them are errors, and all of them are silent. A resource that
 * compares its own file against its own arguments is reading its own homework.
 *
 * `overridden` is the part only `Journald` had, and the tell that it should be shared: it needed it
 * because a drop-in can lose to a later drop-in, which is equally true of `sshd_config.d`, of
 * `config.txt` sections and of Samba's own defaults. Four of the five could report it and one did.
 *
 * **Nothing here reconciles.** A resource that rewrote its own file to win an argument with a file
 * that sorts after it would lose the same argument on the next run. Being told is the whole product.
 */
export interface Resolved<T = Record<string, string>> {
  /** What this resource asked for. */
  declared: T;
  /** What the system reports after everything else has had its say. */
  effective: T;
  /** The declared keys the system does not agree with, sorted so a message reads the same each time. */
  overridden: string[];
}

/**
 * Which declared keys the system disagrees with.
 *
 * A key the system does not mention **at all** is not counted, and that is deliberate rather than
 * lenient: several of these readers are partial by nature. `vcgencmd get_config` does not report
 * `dtoverlay` because the device tree consumes it; a keyword absent from `sshd -T` may be one that
 * version does not know. Reporting those as overridden would fill the field with things nobody can
 * act on, and a report that is mostly noise is one nobody reads — which is the failure this whole
 * package is arranged against.
 *
 * `key` maps a declared name into the alphabet the system answers in, because they are frequently
 * not the same one: `sshd -T` lowercases every keyword, so `PasswordAuthentication` has to be asked
 * for as `passwordauthentication` or it looks absent and would never be compared at all.
 */
export function disagreeing(
  declared: Record<string, string>,
  effective: Record<string, string>,
  key: (declared: string) => string = (name) => name,
): string[] {
  return Object.entries(declared)
    .filter(([name, value]) => {
      const answered = effective[key(name)];
      return answered !== undefined && answered !== value;
    })
    .map(([name]) => name)
    .sort();
}

/**
 * Something this package put at a path on the machine.
 *
 * The shape a consumer needs when it wants "a file at a path" without caring which resource put it
 * there — `ManagedFile` for content this program holds, `Directory` for a place to put things, and
 * out-of-tree resources that write a file from somewhere this package should not know about. One
 * such exists already: it writes key material from a vault and stores only a digest, so nothing
 * secret enters the state file.
 *
 * It describes a **result**, not a source. That is the whole reason it can exist — see the note on
 * the boundary below.
 */
export interface FileOnHost {
  path: pulumi.Output<string>;
  mode: pulumi.Output<string>;
  owner: pulumi.Output<string>;
  group: pulumi.Output<string>;
}

/**
 * **Only data crosses into a provider, never behaviour.**
 *
 * The rule that decides which interfaces are possible in this package at all, and it is not
 * obvious. Pulumi serialises a dynamic provider's whole closure into the state file, and what comes
 * back is evaluated fresh: a value survives that trip, a *running thing* does not. So an interface
 * describing a result — `FileOnHost`, `Resolved` — can be shared freely, and an interface with a
 * `resolve()` method cannot be honoured by anything this package accepts, however well typed.
 *
 * It is why a resource cannot take a *reference* to a secret and fetch it. `{ vault, entry, attr }`
 * would survive serialisation, but only code that already understands those words could act on it,
 * so the package would either learn about one particular vault or grow a plug-in interface with
 * exactly one implementation. The honest alternative — accepting a command to run — is the labelled
 * escape hatch this package deliberately does not have.
 *
 * Three separate failures in one day were the same lesson arriving from three directions: a helper
 * named `fetch` losing its binding and landing on the global, a module-scope import captured by
 * reference, and `promisify` reaching native code. The provider closure is data, not a program.
 */
export const BOUNDARY = 'only data crosses into a provider, never behaviour';
