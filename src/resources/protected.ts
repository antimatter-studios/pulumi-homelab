import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
import { parseFileStat } from './file.ts';

/**
 * A file on the machine whose contents this package never holds.
 *
 * `ManagedFile` takes `content`, which is right for a unit file and wrong for a private key: a
 * resource input is persisted in Pulumi's state, so describing a key that way puts the key in the
 * state file. Encrypted with the stack passphrase, but there — and a secret that exists in two
 * places has two places to leak from.
 *
 * This takes **coordinates instead of content**. The inputs say where to fetch the material from,
 * the provider fetches it at apply time, writes it over ssh, and forgets it. What lands in state is
 * the source, a mode, an owner, and a SHA-256 digest — none of which is a secret, and the last of
 * which is what makes drift detectable anyway.
 *
 * That is the whole idea: **prove it changed without recording what it was.** A digest answers "is
 * the file on the machine still the thing the source holds" completely, and answers "what is the
 * thing" not at all.
 *
 * This works because a Pulumi dynamic provider runs locally, in the process that can reach the
 * source. The material exists in memory on the way through and nowhere else — which is also the one
 * real constraint on `Source`: it names something the *deploying* machine can run, never something
 * the managed machine can.
 */

/**
 * Where the material comes from, as data rather than as a function.
 *
 * A fetcher expressed as a callback was considered and rejected, and the reason is worth keeping.
 * A function cannot be a resource input, so it would have to be captured in the provider closure —
 * which Pulumi serialises into `__provider` in the state file as source text. Then
 * `() => readFileSync(path)` is safe and `() => token` serialises the secret, with nothing in the
 * type system separating the two. That is the single failure this resource exists to prevent,
 * reached by a route that looks like it avoids it.
 *
 * A union is data: it shows up in a preview as a readable diff, it compares properly, it needs no
 * revision bump when it changes, and it can only ever name a source rather than hold a value.
 */
export type Source =
  /** Run it; the material is stdout. Covers `op read`, `pass`, `gpg -d`, `vault kv get`. */
  | { kind: 'exec'; argv: string[] }
  /**
   * Run it; the material is whatever it wrote to `{}`.
   *
   * For the tools that deliberately never put a secret on a pipe — `trove get file` takes `--out`
   * for exactly that reason. The provider makes a private temporary file, substitutes its path for
   * `{}`, and removes it immediately afterwards.
   *
   * `{}` is the same convention `Archive` uses for `healthCommand`, rather than a second spelling
   * invented here.
   */
  | { kind: 'execFile'; argv: string[] }
  /** A file on the deploying machine. The simplest case, and the one with no backup. */
  | { kind: 'file'; path: string };

/**
 * Whether the file is kept as the source says, or only put there once.
 *
 * `always` is the useful default and the right answer for an ssh key: assert the digest, report a
 * hand-edit or a rotated source as drift, rewrite.
 *
 * `once` exists for material the *application* rewrites. A Claude Code `.credentials.json` holds a
 * refresh token that the program rotates; under `always` every refresh reads as drift and the next
 * deployment writes a stale token back, logging the account out. That is two owners of one path,
 * with the application as the second — the failure this package keeps meeting, here reached through
 * a resource whose whole purpose is to be careful.
 *
 * So `once` writes when the path is absent and never rewrites, and its read reports presence rather
 * than content. It still answers exactly the question its write makes — has this been seeded — and
 * deliberately cannot answer what with.
 */
export type Enforcement = 'always' | 'once';

export interface ProtectedFileArgs {
  path: string;
  /** Where the content comes from. Not the content. */
  source: Source;
  mode?: string;
  owner?: string;
  group?: string;
  enforce?: Enforcement;
}

interface ProtectedFileState {
  path: string;
  source: Source;
  mode: string;
  owner: string;
  group: string;
  enforce: Enforcement;
  /**
   * SHA-256 of the content, and deliberately the only thing here derived from it.
   *
   * Comparing digests is what makes this resource honest: it can say the file changed, and it
   * cannot say — to anyone reading the state — what it changed from.
   *
   * Under `once` it holds {@link SEEDED} instead. A digest there would be a promise the resource
   * has decided not to keep: it would report drift on every token refresh and could only "fix" it
   * by undoing one.
   */
  digest: string;
}

const DEFAULTS = { mode: '0600', owner: 'root', group: 'root', enforce: 'once' } as const;

/** What `digest` says when the content is deliberately not being tracked. */
export const SEEDED = 'seeded';

/**
 * `{}` → the temporary file, in every argument that mentions it.
 *
 * `Archive.substitute` does this for one string; a command here is an argv, so that the material's
 * path never goes through a shell and never needs quoting.
 */
export function substituteArgv(argv: string[], where: string): string[] {
  return argv.map((arg) => arg.replace(/\{\}/g, where));
}

/**
 * The refusal for a source that cannot work, said before anything runs.
 *
 * An empty argv reaches `execFileSync` as "no program", whose error names neither the resource nor
 * the field. An `execFile` with no `{}` is worse: the command runs, writes nothing where the
 * provider is looking, and the failure is an empty secret written successfully.
 */
export function sourceRefusal(source: Source): string | undefined {
  if (source.kind === 'file') {
    return source.path ? undefined : 'source.path is empty';
  }
  if (source.argv.length === 0) return `source.argv is empty for kind '${source.kind}'`;
  if (source.kind === 'execFile' && !source.argv.some((arg) => arg.includes('{}'))) {
    return "source.argv for kind 'execFile' has no '{}' for the provider to substitute";
  }
  return undefined;
}

/**
 * Whether the material has to be fetched to answer the question being asked.
 *
 * Under `once` it never does, and that is not an optimisation. Fetching would mean a vault unlocked,
 * a subprocess run, and a secret in memory, in order to compute an answer that is discarded — and on
 * a `preview`, which is the command people run expecting it to touch nothing.
 */
export function needsMaterial(enforce: Enforcement): boolean {
  return enforce === 'always';
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<ProtectedFileArgs, ProtectedFileState> {
  /**
   * Take the material from wherever it lives, locally, without it touching disk where it can be
   * read.
   *
   * Named `collect` rather than `fetch`, and that is not style. Pulumi serialises this provider's
   * closure into the state file, and a local helper called `fetch` does not survive the trip — the
   * call then lands on Node's global `fetch`, which reports `Failed to parse URL from [object
   * Object]`. Nothing is undefined and the name resolves either way, which is what makes it take an
   * afternoon to find.
   */
  const collect = async (source: Source): Promise<Buffer> => {
    const refusal = sourceRefusal(source);
    if (refusal) throw new Error(`ProtectedFile: ${refusal}`);

    const { execFileSync } = await import('node:child_process');
    const { mkdtempSync, readFileSync, rmSync } = await import('node:fs');
    const { tmpdir } = await import('node:os');
    const { join } = await import('node:path');

    if (source.kind === 'file') return readFileSync(source.path);

    if (source.kind === 'exec') {
      const [program, ...rest] = source.argv;
      return Buffer.from(
        execFileSync(program as string, rest, { encoding: 'buffer', timeout: 30_000, stdio: ['ignore', 'pipe', 'pipe'] }),
      );
    }

    // execFile: a directory only this user can enter, and removed immediately — the bytes are on
    // disk for the moment between those two steps and nowhere else.
    const dir = mkdtempSync(join(tmpdir(), 'protected-'));
    const out = join(dir, 'blob');
    try {
      const [program, ...rest] = substituteArgv(source.argv, out);
      execFileSync(program as string, rest, { encoding: 'utf8', timeout: 30_000, stdio: ['ignore', 'pipe', 'pipe'] });
      return readFileSync(out);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  };

  const digestOf = async (content: Buffer): Promise<string> => {
    const { createHash } = await import('node:crypto');
    return createHash('sha256').update(content).digest('hex');
  };

  /**
   * Write it without the content ever appearing in an argument list.
   *
   * Base64 on the way over and decoded on the machine: a heredoc would work for text but mangles
   * anything binary, and putting the material in argv would show it to every process that can run
   * `ps`. `umask 077` before the redirect, so there is no window in which the file is readable by
   * anyone else — the mode and owner are applied afterwards, to a file that was never loose.
   */
  const write = async (state: ProtectedFileState, content: Buffer): Promise<void> => {
    const parent = state.path.replace(/\/[^/]*$/, '') || '/';
    await must(host, escalate(host,
      `set -e; install -d -m 0755 ${shellQuote(parent)}; ` +
      `umask 077; printf '%s' ${shellQuote(content.toString('base64'))} | base64 -d > ${shellQuote(state.path)}; ` +
      `chmod ${state.mode} ${shellQuote(state.path)}; ` +
      `chown ${shellQuote(`${state.owner}:${state.group}`)} ${shellQuote(state.path)}`,
    ));
  };

  /** The mode and ownership alone, for the case where the content is not ours to rewrite. */
  const reface = async (state: ProtectedFileState): Promise<void> => {
    await must(host, escalate(host,
      `set -e; chmod ${state.mode} ${shellQuote(state.path)}; ` +
      `chown ${shellQuote(`${state.owner}:${state.group}`)} ${shellQuote(state.path)}`,
    ));
  };

  return {
    async create(args) {
      const wanted = { ...DEFAULTS, ...args } as ProtectedFileState;
      const content = await collect(args.source);
      wanted.digest = needsMaterial(wanted.enforce) ? await digestOf(content) : SEEDED;
      await write(wanted, content);
      return { id: args.path, outs: wanted };
    },

    async read(id, state) {
      const previous = state as ProtectedFileState | undefined;
      const enforce = previous?.enforce ?? DEFAULTS.enforce;

      // Exit 9 is "no such file", said by the test rather than inferred from a message. An absent
      // file is an absent resource, which under `once` is precisely what makes it seed again.
      const asked = await ask(host, escalate(host,
        `test -f ${shellQuote(id)} || exit 9; ` +
        (needsMaterial(enforce) ? `sha256sum ${shellQuote(id)} | cut -d' ' -f1; ` : '') +
        `stat -c '%a %U %G' ${shellQuote(id)}`,
      ));
      if (asked.code === 9) return { id: undefined, props: undefined };
      if (asked.code !== 0) throw new Error(`could not read ${id}: ${asked.err.trim()}`);

      const lines = asked.out.trim().split('\n');
      const digest = needsMaterial(enforce) ? (lines.shift() ?? '') : SEEDED;
      const { mode, owner, group } = parseFileStat(lines.shift() ?? '');

      return {
        id,
        props: {
          ...DEFAULTS,
          source: previous?.source as Source,
          ...previous,
          path: id,
          enforce,
          digest,
          mode,
          owner,
          group,
        },
      };
    },

    async update(id, _old, args) {
      const wanted = { ...DEFAULTS, ...args, path: id } as ProtectedFileState;

      // Under `once` the content belongs to whatever writes it now, and an update here is only ever
      // about mode or ownership. Rewriting would undo a rotation — the thing this mode exists to
      // avoid — so the material is not even fetched.
      if (!needsMaterial(wanted.enforce)) {
        wanted.digest = SEEDED;
        await reface(wanted);
        return { outs: wanted };
      }

      const content = await collect(args.source);
      wanted.digest = await digestOf(content);
      await write(wanted, content);
      return { outs: wanted };
    },

    async diff(_id, old, args) {
      const wanted = { ...DEFAULTS, ...args };
      const facing = old.mode !== wanted.mode || old.owner !== wanted.owner || old.group !== wanted.group;

      // Asking the source what it holds now is the whole of drift detection under `always`, and
      // exactly the wrong thing to do under `once`: see `needsMaterial`.
      const content = needsMaterial(wanted.enforce)
        && old.digest !== await digestOf(await collect(args.source));

      return {
        changes: transportChanged(old)
          || facing
          || content
          || old.enforce !== wanted.enforce
          || old.path !== args.path,
        // A secret at a new path is a new secret; rewriting one in place is not.
        replaces: old.path !== args.path ? ['path'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      await must(host, escalate(host, `rm -f ${shellQuote(id)}`));
    },
  };
}

/** A secret file on the machine, described by where it comes from rather than by what it says. */
export class ProtectedFile extends pulumi.dynamic.Resource {
  declare readonly digest: pulumi.Output<string>;
  declare readonly path: pulumi.Output<string>;

  constructor(name: string, host: Target, args: ProtectedFileArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { digest: undefined, ...DEFAULTS, ...args },
      withLegacyAlias(opts), 'homelab', 'ProtectedFile');
  }
}
