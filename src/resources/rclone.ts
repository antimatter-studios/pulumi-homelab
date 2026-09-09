import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * An rclone remote — a named backend in rclone's config file.
 *
 * The same shape as `SambaShare` and `Journald`: a subsystem on the machine, configured to do a
 * job, with settings keyed exactly as the thing itself spells them. rclone has around seventy
 * backends and hundreds of options between them, so named arguments would need a release every time
 * somebody wanted one this package had not heard of.
 *
 * **The trap that makes this a resource rather than a `ManagedFile` with careful content.** rclone
 * stores passwords *obscured*, and `rclone obscure` is **not deterministic**: it encrypts with a
 * random initialisation vector, so obscuring the same password twice gives two different strings.
 * A resource that obscured the desired password and compared it against what is stored would report
 * drift on every refresh, for ever, on a remote nobody had touched — the mode-normalisation bug
 * wearing a cryptographic hat.
 *
 * So the comparison happens in plaintext. `rclone reveal` turns what is stored back into what it
 * was, and that is what gets compared with the argument. The cost is that the plaintext lives in
 * the Pulumi state file, which is why `secrets` are marked as secret outputs.
 *
 * **Obscuring is obfuscation, not encryption**, and it is worth being plain about that: the key is
 * a constant in rclone's own source, so anybody holding the file holds the passwords. It stops
 * somebody reading over your shoulder and nothing else. A remote's credentials are protected by the
 * config file's mode and by nothing else, which is a reason to put the file somewhere only the
 * account that needs it can read.
 */
export interface RcloneRemoteArgs {
  /** The remote's name, as it appears in `[brackets]` and before the colon in a path. */
  remote: string;
  /** The backend: `sftp`, `s3`, `webdav`, and so on. */
  type: string;
  /** Everything that is not a credential, keyed as rclone spells it. */
  settings?: Record<string, string>;
  /**
   * Credentials, given in plaintext and stored obscured.
   *
   * Separate from `settings` because they are handled differently at both ends: obscured on the way
   * in, revealed on the way back out so that a comparison is possible at all. They are marked as
   * secret outputs, but the state file still holds them in plaintext — there is nowhere else for
   * them to be if drift is ever to be detected.
   */
  secrets?: Record<string, string>;
  /**
   * Which config file to write.
   *
   * Worth setting rather than leaving to rclone, which picks a path from the environment of
   * whichever account is running it. This provider escalates, so the default would be root's — and
   * a systemd unit running as somebody else would then read a config that does not exist.
   */
  config?: string;
}

interface RcloneRemoteState {
  remote: string;
  type: string;
  settings: Record<string, string>;
  secrets: Record<string, string>;
  config: string;
}

const DEFAULTS = { config: '/root/.config/rclone/rclone.conf' };

/** `rclone config dump` is JSON: every remote, by name, with its stored values. */
export function parseDump(out: string, remote: string): Record<string, string> | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(out);
  } catch {
    return null;
  }
  const all = parsed as Record<string, Record<string, string> | undefined>;
  return all[remote] ?? null;
}

/**
 * The `key value` pairs for `rclone config create`, in a stable order.
 *
 * Sorted because the order of an object's keys is the order they were written in, and a settings
 * map rearranged in the source would otherwise produce a different command for an identical remote.
 */
export function configPairs(settings: Record<string, string>): string[] {
  return Object.keys(settings)
    .sort()
    .flatMap((key) => [key, settings[key] ?? '']);
}

/** What is actually stored, with the credentials turned back into what they were. */
export async function readRemote(
  host: Target,
  remote: string,
  secretKeys: string[],
  config = DEFAULTS.config,
): Promise<{ type: string; settings: Record<string, string>; secrets: Record<string, string> } | null> {
  const where = `--config ${shellQuote(config)}`;
  const dumped = await ask(host, escalate(host, `rclone ${where} config dump 2>/dev/null`));
  if (dumped.code !== 0) return null;
  const stored = parseDump(dumped.out, remote);
  if (!stored) return null;

  const secrets: Record<string, string> = {};
  for (const key of secretKeys) {
    const obscured = stored[key];
    if (obscured === undefined) continue;
    // reveal rather than obscure-and-compare: obscuring is not deterministic, so comparing the
    // stored value against a freshly obscured one reports drift for ever on an untouched remote
    const revealed = await ask(host, escalate(host, `rclone reveal ${shellQuote(obscured)} 2>/dev/null`));
    if (revealed.code === 0) secrets[key] = revealed.out.replace(/\n$/, '');
  }

  const settings: Record<string, string> = {};
  for (const [key, value] of Object.entries(stored)) {
    if (key === 'type' || secretKeys.includes(key)) continue;
    settings[key] = value;
  }
  return { type: stored.type ?? '', settings, secrets };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<RcloneRemoteArgs, RcloneRemoteState> {
  const settle = async (args: RcloneRemoteArgs): Promise<RcloneRemoteState> => {
    const config = args.config ?? DEFAULTS.config;
    const settings = args.settings ?? {};
    const secrets = args.secrets ?? {};
    const secretKeys = Object.keys(secrets);

    const current = await readRemote(host, args.remote, secretKeys, config);
    const same = current
      && current.type === args.type
      && JSON.stringify(current.settings) === JSON.stringify(settings)
      && secretKeys.every((key) => current.secrets[key] === secrets[key]);
    // an update that changes nothing does nothing: this resource updates whenever the provider is
    // upgraded, and rewriting a config file to the same values is churn on a file holding passwords
    if (!same) {
      const where = `--config ${shellQuote(config)}`;
      const pairs = [...configPairs(settings), ...configPairs(secrets)].map(shellQuote).join(' ');
      // --obscure asks rclone to obscure the values that its own backend schema says are passwords,
      // rather than this code guessing which of them are
      await must(host, escalate(host,
        `mkdir -p ${shellQuote(config.replace(/\/[^/]*$/, '') || '/')} && ` +
        `rclone ${where} config create ${shellQuote(args.remote)} ${shellQuote(args.type)} ${pairs} ` +
        `--obscure --non-interactive >/dev/null`,
      ));
    }

    const actual = await readRemote(host, args.remote, secretKeys, config);
    if (!actual) throw new Error(`wrote the remote ${args.remote} but rclone does not report it`);
    return { remote: args.remote, type: actual.type, settings: actual.settings, secrets, config };
  };

  return {
    async create(args) {
      return { id: args.remote, outs: await settle(args) };
    },

    async read(id, state) {
      const config = state?.config ?? DEFAULTS.config;
      const secretKeys = Object.keys(state?.secrets ?? {});
      const actual = await readRemote(host, id, secretKeys, config);
      // gone from the config: Pulumi forgets it and the next up puts it back
      if (!actual) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          config,
          ...state,
          remote: id,
          // the machine's answer wins over remembered state, and the secrets come back revealed so
          // the comparison happens in plaintext at both ends
          type: actual.type,
          settings: actual.settings,
          secrets: { ...state?.secrets, ...actual.secrets },
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, remote: id }) };
    },

    async diff(_id, old, args) {
      const settings = args.settings ?? {};
      const secrets = args.secrets ?? {};
      return {
        changes: transportChanged(old)
          || old.type !== args.type
          || old.remote !== args.remote
          || old.config !== (args.config ?? DEFAULTS.config)
          || JSON.stringify(old.settings) !== JSON.stringify(settings)
          // compared in plaintext on both sides, which is the whole point
          || JSON.stringify(old.secrets) !== JSON.stringify(secrets),
        replaces: old.remote !== args.remote ? ['remote'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // the remote goes; nothing it ever pointed at is touched, and nothing mounted from it is
      // unmounted — a mount is a systemd unit's business, and it will fail loudly on its own
      await must(host, escalate(host, `rclone config delete ${shellQuote(id)} 2>/dev/null || true`));
    },
  };
}

/** A remote rclone should know about, read back in plaintext so drift is detectable at all. */
export class RcloneRemote extends pulumi.dynamic.Resource {
  declare readonly remote: pulumi.Output<string>;
  declare readonly type: pulumi.Output<string>;

  constructor(name: string, host: Target, args: RcloneRemoteArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, { settings: {}, secrets: {}, config: DEFAULTS.config, ...args }, withLegacyAlias({
      // the plaintext is in the state file because there is nowhere else for it to be if drift is
      // to be detected; marking it at least keeps it out of a preview somebody is watching
      additionalSecretOutputs: ['secrets'],
      ...opts,
    }), 'homelab', 'RcloneRemote');
  }
}
