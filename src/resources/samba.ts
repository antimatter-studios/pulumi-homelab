import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';
import { disagreeing } from '../resolved.ts';

/**
 * Samba shares and users, read back through Samba's own understanding of its configuration.
 *
 * `testparm -s` is the `read` here, and it is a better one than reading `smb.conf` would be: it
 * prints the **effective** configuration as smbd resolves it — defaults applied, includes followed,
 * syntax validated. So a share somebody added by hand appears, a value that is subtly wrong appears
 * as what Samba actually made of it rather than what the file says, and a configuration that will
 * not parse fails at read time instead of at restart time on a machine nobody is watching.
 *
 * **Sections are edited in place; the file is never regenerated.** Samba has no `conf.d` that globs
 * — `include =` takes no wildcards — so the choice is between one resource owning the whole
 * `smb.conf` and each share being edited into it surgically. Owning the file would mean this
 * provider could not coexist with a hand-written share, which is the opposite of the thing that
 * makes it safe to point at a server somebody has been running for years. So a declared section is
 * replaced where it exists and appended where it does not, and every other byte of the file —
 * including sections nothing here declares, and including the comments — comes back untouched.
 *
 * **Reload, never restart.** `smbcontrol all reload-config` re-reads the configuration without
 * dropping connections; restarting smbd kills transfers in progress, which on a machine somebody
 * streams from is a film stopping in the middle. Same reasoning that keeps `KernelCmdline` from
 * rebooting: the machine has a life that is not this deployment.
 */
export interface SambaShareArgs {
  /** The section name, as it appears in brackets: `public`, not `[public]`. */
  share: string;
  /** What is shared. Named separately because every share has one and it is what people look for. */
  path: string;
  /**
   * Everything else, spelled exactly as `smb.conf` spells it — keys have spaces in them:
   * `{ 'read only': 'no', 'valid users': 'admin', 'guest ok': 'yes' }`.
   *
   * A map rather than named arguments because that is the shape of the data, and because Samba has
   * several hundred parameters. `testparm` is what catches a misspelling: an unknown key does not
   * survive into the effective configuration, so it shows up as a setting that never took.
   */
  settings?: Record<string, string>;
  /**
   * Which configuration file to edit.
   *
   * An argument because the location is a packaging decision rather than a fact: Debian puts it at
   * `/etc/samba/smb.conf`, and a machine that keeps it elsewhere is not this package's business to
   * be wrong about.
   */
  config?: string;
}

interface SambaShareState {
  share: string;
  path: string;
  settings: Record<string, string>;
  config: string;
  /** What Samba itself reports for this section, defaults resolved. */
  effective: Record<string, string>;
  /** Declared settings Samba resolved to something else. Reported, never reconciled. */
  overridden: string[];
}

const SMB_CONF = '/etc/samba/smb.conf';

/**
 * Split an ini-shaped file into sections, keeping each section's lines verbatim.
 *
 * Verbatim because this is used for both halves of the job: reading Samba's own output, and taking
 * a file apart in order to put nearly all of it back unchanged.
 */
export function parseSections(text: string): Map<string, string[]> {
  const sections = new Map<string, string[]>();
  let current: string | null = null;
  for (const line of text.split('\n')) {
    const heading = line.trim().match(/^\[(.+)\]$/);
    if (heading) {
      current = heading[1] ?? null;
      if (current !== null && !sections.has(current)) sections.set(current, []);
      continue;
    }
    if (current !== null) sections.get(current)?.push(line);
  }
  return sections;
}

/** The `key = value` pairs of one section, ignoring comments and blank lines. */
export function parseShareSettings(lines: string[]): Record<string, string> {
  const settings: Record<string, string> = {};
  for (const line of lines) {
    const text = line.trim();
    if (text.length === 0 || text.startsWith('#') || text.startsWith(';')) continue;
    const equals = text.indexOf('=');
    if (equals <= 0) continue;
    // Samba's own output is indented and space-padded; the keys have spaces inside them too, so
    // only the whitespace at the ends is noise
    settings[text.slice(0, equals).trim()] = text.slice(equals + 1).trim();
  }
  return settings;
}

/** One section of `testparm -s` output, as Samba understands it. */
export function effectiveShare(testparm: string, share: string): Record<string, string> | null {
  const lines = parseSections(testparm).get(share);
  return lines ? parseShareSettings(lines) : null;
}

/** The section as it should appear in the file. */
export function shareSection(share: string, path: string, settings: Record<string, string>): string {
  const body = Object.entries({ path, ...settings }).map(([key, value]) => `   ${key} = ${value}`);
  return [`[${share}]`, ...body].join('\n');
}

/**
 * Put one section into the file and leave every other byte of it alone.
 *
 * Replaced in place where it exists, appended where it does not — the same discipline as
 * `FstabEntry`, for the same reason. A section that migrates to the end of the file on every edit
 * makes a diff of the file useless for seeing what actually changed, and a regenerated `smb.conf`
 * would silently discard every share the code does not know about.
 */
export function upsertSection(text: string, share: string, section: string): string {
  const lines = text.split('\n');
  const start = lines.findIndex((line) => line.trim() === `[${share}]`);
  if (start < 0) {
    return `${text.replace(/\n+$/, '')}\n\n${section}\n`;
  }
  // to the next heading, or to the end of the file
  let end = start + 1;
  while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;
  return [...lines.slice(0, start), ...section.split('\n'), ...lines.slice(end)].join('\n');
}

/** Take one section out, and nothing else with it. */
export function removeSection(text: string, share: string): string {
  const lines = text.split('\n');
  const start = lines.findIndex((line) => line.trim() === `[${share}]`);
  if (start < 0) return text;
  let end = start + 1;
  while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;
  return [...lines.slice(0, start), ...lines.slice(end)].join('\n');
}

/** What Samba makes of the configuration right now. */
export async function readShare(host: Target, share: string, config = SMB_CONF): Promise<Record<string, string> | null> {
  // -s suppresses the prompt; stderr carries testparm's commentary and is not the answer
  const asked = await ask(host, escalate(host, `testparm -s ${shellQuote(config)} 2>/dev/null`));
  if (asked.code !== 0) throw new Error(`samba's own configuration does not parse: ${asked.err.trim()}`);
  return effectiveShare(asked.out, share);
}

/**
 * Write the file, but only once testparm agrees it is a Samba configuration.
 *
 * The validation is the whole reason this is not a `ManagedFile` with careful content: a broken
 * `smb.conf` does not break the share it is in, it stops smbd reloading, and the symptom is a share
 * that quietly does not exist. Checking a candidate file first turns that into a failed deployment
 * with a message.
 */
async function apply(host: Target, updated: string, config = SMB_CONF): Promise<void> {
  await must(host, escalate(host,
    `staging=$(mktemp) && ` +
    `${heredocInto('"$staging"', updated)}\n` +
    `testparm -s "$staging" >/dev/null 2>&1 || { rm -f "$staging"; echo "the resulting smb.conf does not parse" >&2; exit 1; }; ` +
    `install -m 0644 -o root -g root "$staging" ${shellQuote(config)} && rm -f "$staging" && ` +
    // reload rather than restart: a restart drops every connection, which on this machine means
    // whatever somebody is watching stops in the middle
    `smbcontrol all reload-config >/dev/null 2>&1 || true`,
  ));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SambaShareArgs, SambaShareState> {
  const settle = async (args: SambaShareArgs): Promise<SambaShareState> => {
    const settings = args.settings ?? {};
    const config = args.config ?? SMB_CONF;
    const current = await must(host, escalate(host, `cat ${shellQuote(config)}`));
    const updated = upsertSection(current, args.share, shareSection(args.share, args.path, settings));
    // only when the file would actually differ: an update caused by this package being upgraded
    // should not rewrite smb.conf and reload smbd on every machine
    if (updated !== current) await apply(host, updated, config);
    const effective = await readShare(host, args.share, config);
    if (!effective) throw new Error(`wrote the [${args.share}] section but samba does not report it`);
    return {
      share: args.share, path: args.path, settings, config, effective,
      overridden: disagreeing({ path: args.path, ...settings }, effective),
    };
  };

  return {
    async create(args) {
      return { id: args.share, outs: await settle(args) };
    },

    async read(id, state) {
      const config = state?.config ?? SMB_CONF;
      const effective = await readShare(host, id, config);
      if (!effective) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          settings: state?.settings ?? {},
          config,
          ...state,
          ...state,
          share: id,
          // what Samba says the path is, which is the answer that matters when they disagree
          path: effective.path ?? state?.path ?? '',
          effective,
          overridden: disagreeing(state?.settings ?? {}, effective),
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, share: id }) };
    },

    async diff(_id, old, args) {
      const settings = args.settings ?? {};
      // compared against what Samba reports rather than against the last arguments: a share edited
      // by hand is drift, and testparm is the only thing that knows what the edit actually meant
      const wanted = { path: args.path, ...settings };
      const differs = Object.entries(wanted).some(([key, value]) => old.effective?.[key] !== value);
      return {
        changes: providerChanged(old, args)
          || differs || old.share !== args.share,
        replaces: old.share !== args.share ? ['share'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      const current = await must(host, escalate(host, `cat ${shellQuote(SMB_CONF)}`));
      await apply(host, removeSection(current, id));
    },
  };
}

/** A Samba share, checked against `testparm` rather than against the file it was written to. */
export class SambaShare extends pulumi.dynamic.Resource {
  declare readonly share: pulumi.Output<string>;
  declare readonly effective: pulumi.Output<Record<string, string>>;

  constructor(name: string, host: Target, args: SambaShareArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, { effective: undefined, overridden: undefined, settings: {}, config: SMB_CONF, ...args }, withLegacyAlias(opts), 'homelab', 'SambaShare');
  }
}

/**
 * A Samba account.
 *
 * **This is the one resource in the package that cannot fully honour the rule, and it is worth
 * saying so rather than pretending.** The password lives hashed in Samba's own database and cannot
 * be read back, so `read` can report that the account exists and nothing about whether its password
 * is the one the code says. Changing the password in the program will set it; changing it on the
 * machine will not come back as drift, because there is nothing to compare.
 *
 * It composes with `User` rather than replacing it: Samba maps its accounts onto Unix ones, so the
 * Unix account has to exist first. Declare both and let the dependency edge say so.
 */
export interface SambaUserArgs {
  /** The Unix account this maps onto. It must already exist. */
  name: string;
  /**
   * The password to set.
   *
   * Marked secret in state, because it arrives in plain text and is written into the Pulumi state
   * file like everything else. It cannot be read back from the machine, so this is the only record
   * of it that exists — treat the state file accordingly.
   */
  password: pulumi.Input<string>;
}

interface SambaUserState {
  name: string;
  password: string;
  /** Whether `pdbedit` knows the account. The only half of this resource that can be read. */
  exists: boolean;
}

/** The accounts Samba knows about, out of `pdbedit -L` (`name:uid:` per line). */
export function parseSambaUsers(out: string): string[] {
  return out
    .split('\n')
    .map((line) => line.split(':')[0]?.trim() ?? '')
    .filter((name) => name.length > 0);
}

/** Whether Samba has that account. */
export async function readSambaUser(host: Target, name: string): Promise<boolean> {
  const asked = await ask(host, escalate(host, 'pdbedit -L 2>/dev/null'));
  if (asked.code !== 0) return false;
  return parseSambaUsers(asked.out).includes(name);
}

function userProviderFor(host: Target): pulumi.dynamic.ResourceProvider<{ name: string; password: string }, SambaUserState> {
  const setPassword = async (name: string, password: string): Promise<void> => {
    // -s reads both the password and its confirmation from stdin, which keeps it off the command
    // line and out of the process table where every user on the machine could read it
    await must(host, escalate(host,
      `printf '%s\\n%s\\n' ${shellQuote(password)} ${shellQuote(password)} | smbpasswd -s -a ${shellQuote(name)} >/dev/null`,
    ));
  };

  return {
    async create(args) {
      const unix = await ask(host, `getent passwd ${shellQuote(args.name)}`);
      if (unix.code !== 0) {
        throw new Error(`there is no unix account called ${args.name}; samba maps onto one, so declare a User first`);
      }
      await setPassword(args.name, args.password);
      return { id: args.name, outs: { name: args.name, password: args.password, exists: true } };
    },

    async read(id, state) {
      const exists = await readSambaUser(host, id);
      if (!exists) return { id: undefined, props: undefined };
      // the password is carried forward from state because the machine cannot be asked. This is the
      // honest shape of a value that is write-only, rather than a comparison that would always pass
      return { id, props: { password: state?.password ?? '', ...state, name: id, exists } };
    },

    async update(id, _old, args) {
      await setPassword(id, args.password);
      return { outs: { name: id, password: args.password, exists: true } };
    },

    async diff(_id, old, args) {
      return {
        // a changed password is a change we apply without being able to verify; a changed name is a
        // different account
        changes: providerChanged(old, args)
          || old.password !== args.password || old.name !== args.name,
        replaces: old.name !== args.name ? ['name'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      await must(host, escalate(host, `smbpasswd -x ${shellQuote(id)} >/dev/null 2>&1 || true`));
    },
  };
}

/** A Samba account, whose existence can be read and whose password cannot. */
export class SambaUser extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly exists: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: SambaUserArgs, opts?: pulumi.CustomResourceOptions) {
    super(userProviderFor(host), name, { exists: undefined, ...args }, withLegacyAlias({
      // the password is in the state file whatever happens; marking it means it is at least masked
      // in previews and in the console rather than printed to whoever is watching a deployment
      additionalSecretOutputs: ['password'],
      ...opts,
    }), 'homelab', 'SambaUser');
  }
}
