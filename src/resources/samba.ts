import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
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

/**
 * A parameter name, in the one form comparisons can use.
 *
 * Samba itself ignores whitespace inside a parameter name — `netbios name` and `Netbios   Name` are
 * the same key — and a file written by hand is aligned with whatever spacing somebody liked.
 * Comparing the names verbatim means a hand-aligned file keeps its old value and gains a second
 * line saying something different, with Samba taking whichever it reads last.
 */
const sameKey = (a: string, b: string) =>
  a.trim().toLowerCase().replace(/\s+/g, ' ') === b.trim().toLowerCase().replace(/\s+/g, ' ');

/**
 * Whether a declared value and the one Samba reports are the same value.
 *
 * **This is why `SambaShare` reported an update on every deployment for ever.** `testparm` does not
 * echo what the file says — it prints Samba's own resolution of it, and Samba's vocabulary is not
 * the file's:
 *
 * ```
 * smb.conf:      guest ok = yes        read only = no
 * testparm -s:   guest ok = Yes        read only = No
 * ```
 *
 * Compared verbatim those differ on every run, so the resource updated on every run — reloading
 * Samba each time, for ever, on a machine nobody had touched. The evidence was in this package's own
 * test fixture, which held both spellings side by side and never compared them.
 *
 * That is the fourth instance of one bug: `stat` answering `644` where the code says `0644`, `sshd
 * -T` printing `without-password` for `prohibit-password`, `rclone obscure` never returning the same
 * string twice. **The read is accurate and is not in the same alphabet as the write.**
 *
 * Booleans are compared as booleans, because Samba accepts several spellings of each and means one
 * thing by them. Names Samba case-folds — NetBIOS names and workgroups are always uppercased — are
 * compared case-insensitively. Everything else is compared exactly, because a path differing only in
 * case is a different path.
 */
// Arrays rather than Sets, and this is not a style choice. A module-scope constant is *captured* by
// the provider closure, and a `Set` does not survive being serialised into the state file: it comes
// back as a plain `{}`. The identifier still resolves, `has` is still found on Object's prototype
// chain, and it throws only when called — `Method Set.prototype.has called on incompatible receiver`,
// from inside a diff, aborting every preview partway through. Which looked like flakiness, because
// the run died at a different point each time and reported a different count of unchanged resources.
//
// A Set looks like data and is not: its usefulness is entirely in its prototype, and only data
// crosses into a provider. A Set built *inside* a function is fine — it is constructed fresh when
// the revived code runs — and this package has several of those. It is the captured ones that lie.
const YES = ['yes', 'true', '1'];
const NO = ['no', 'false', '0'];
const CASE_FOLDED = ['netbios name', 'workgroup', 'server string', 'realm'];

export function sambaSameValue(key: string, declared: string, effective: string): boolean {
  const a = declared.trim();
  const b = effective.trim();
  if (a === b) return true;
  const lowerA = a.toLowerCase();
  const lowerB = b.toLowerCase();
  if (YES.includes(lowerA) && YES.includes(lowerB)) return true;
  if (NO.includes(lowerA) && NO.includes(lowerB)) return true;
  const folded = CASE_FOLDED.some((name) => sameKey(name, key));
  return folded && lowerA === lowerB;
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

/**
 * Put one `key = value` into one section, leaving every other line of it alone.
 *
 * The third instance of an idiom this package already has twice — `BootConfig` edits one line of
 * `config.txt`, `Journald` one key of a drop-in. It exists because owning a whole section means
 * owning every setting in it, and `[global]` on a machine that has been tuned by hand is dozens of
 * settings nobody can reproduce from memory. Refusing to declare the section is correct; being
 * unable to change one key in it is not.
 */
export function upsertSetting(text: string, share: string, key: string, value: string): string {
  const lines = text.split('\n');
  const header = lines.findIndex((line) => line.trim() === `[${share}]`);
  if (header < 0) {
    return `${text.replace(/\n+$/, '')}\n\n[${share}]\n   ${key} = ${value}\n`;
  }
  let end = header + 1;
  while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;

  const body = lines.slice(header + 1, end);
  // matched on the key alone, case-insensitively and ignoring the spaces around it, because smb.conf
  // keys contain spaces and are written with whatever alignment somebody liked
  const at = body.findIndex((line) => {
    const text_ = line.trim();
    if (text_.length === 0 || text_.startsWith('#') || text_.startsWith(';')) return false;
    const equals = text_.indexOf('=');
    return equals > 0 && sameKey(text_.slice(0, equals), key);
  });
  const wanted = `   ${key} = ${value}`;
  if (at >= 0) body[at] = wanted;
  else body.push(wanted);
  return [...lines.slice(0, header + 1), ...body, ...lines.slice(end)].join('\n');
}

/** Take one `key` out of one section, and nothing else. */
export function removeSetting(text: string, share: string, key: string): string {
  const lines = text.split('\n');
  const header = lines.findIndex((line) => line.trim() === `[${share}]`);
  if (header < 0) return text;
  let end = header + 1;
  while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;
  const body = lines.slice(header + 1, end).filter((line) => {
    const text_ = line.trim();
    if (text_.length === 0 || text_.startsWith('#') || text_.startsWith(';')) return true;
    const equals = text_.indexOf('=');
    return !(equals > 0 && sameKey(text_.slice(0, equals), key));
  });
  return [...lines.slice(0, header + 1), ...body, ...lines.slice(end)].join('\n');
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

/**
 * What Samba makes of the configuration right now.
 *
 * **`-sv`, not `-s`, and the difference is a bug that took five applies to find.** `testparm -s`
 * prints only what differs from Samba's defaults — so a setting whose declared value *equals* its
 * default is omitted entirely. Not reported wrongly: absent. The comparison against the declared
 * value can then never succeed, and the resource updates for ever.
 *
 * Which is easiest to see with `netbios name`, because Samba derives its default from the hostname:
 * declaring `netbios name = homelab` on a machine called `homelab` sets it to exactly its own
 * default, and testparm stops printing it. **A resource that successfully makes a setting match the
 * default becomes permanently unable to observe that it did.**
 *
 * So the absence is not a hole to be worked around — it is Samba saying "this is the default", and
 * `-v` is how to ask what the default is. It prints every parameter, which is a few hundred lines
 * over a connection that already sends whole files, and callers keep only the keys they asked
 * about so nothing bloats the state file.
 */
export async function readShare(host: Target, share: string, config = SMB_CONF): Promise<Record<string, string> | null> {
  const asked = await ask(host, escalate(host, testparmCommand(config)));
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
    const resolved = await readShare(host, args.share, config);
    if (!resolved) throw new Error(`wrote the [${args.share}] section but samba does not report it`);
    const wanted = { path: args.path, ...settings };
    return {
      share: args.share,
      path: args.path,
      settings,
      config,
      effective: narrowTo(resolved, Object.keys(wanted)),
      overridden: overriddenIn(wanted, resolved),
    };
  };

  return {
    async create(args) {
      return { id: args.share, outs: await settle(args) };
    },

    async read(id, state) {
      const config = state?.config ?? SMB_CONF;
      const resolved = await readShare(host, id, config);
      if (!resolved) return { id: undefined, props: undefined };
      const asked = { path: state?.path ?? '', ...(state?.settings ?? {}) };
      const effective = narrowTo(resolved, Object.keys(asked));
      return {
        id,
        props: {
          settings: state?.settings ?? {},
          config,
          ...state,
          ...state,
          share: id,
          // what Samba says the path is, which is the answer that matters when they disagree
          path: resolved.path ?? state?.path ?? '',
          effective,
          overridden: overriddenIn(state?.settings ?? {}, resolved),
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
      // compared through Samba's own vocabulary rather than verbatim: `yes` and `Yes` are one value,
      // and comparing them as strings is an update on every deployment for ever
      const differs = Object.entries(wanted).some(([key, value]) => {
        const answered = Object.entries(old.effective ?? {}).find(([name]) => sameKey(name, key))?.[1];
        return answered === undefined || !sambaSameValue(key, value, answered);
      });
      return {
        changes: transportChanged(old)
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
    super(stamped(providerFor(host)), name, { effective: undefined, overridden: undefined, settings: {}, config: SMB_CONF, ...args }, withLegacyAlias(opts), 'homelab', 'SambaShare');
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
        changes: transportChanged(old)
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

/**
 * One setting, in one section of `smb.conf`.
 *
 * `SambaShare` owns a whole section, which is right for a share you declared and wrong for
 * `[global]`: on a machine that has been tuned by hand that is dozens of settings nobody can
 * reproduce from memory, and owning the section means owning all of them. Refusing to declare it is
 * correct. Being unable to change one key in it is not, so this is the surgical form — the same
 * idiom as `BootConfig` editing one line of `config.txt`.
 *
 * **The reload question has to be answered per key, and `netbios name` is why.** Sharing settings
 * are re-read by `smbcontrol all reload-config` without dropping a connection, which is what
 * `SambaShare` does and is right for it. But **NetBIOS name registration happens when nmbd
 * starts**, not when it re-reads its configuration — so a reload leaves the old name registered,
 * the command exits zero, and the network goes on answering to a name the code says is gone. That
 * is a resource reporting success while the thing somebody asked for has not happened.
 *
 * Worth knowing for the same key: Samba's `mdns name` defaults to `netbios`, so this one setting
 * moves what is advertised on `_smb._tcp` as well as the SMB name itself.
 */
export interface SambaSettingArgs {
  /** Which section, in brackets in the file: `global`, not `[global]`. */
  share: string;
  /** The key, spelled as `smb.conf` spells it — spaces and all: `netbios name`. */
  key: string;
  value: string;
  /**
   * How to make Samba notice.
   *
   * - `reload` re-reads the configuration without dropping connections. Right for share settings.
   * - `nmbd` restarts the name daemon as well, which is what `netbios name` needs: a reload leaves
   *   the previous name registered.
   * - `all` restarts smbd too, which **drops open connections** — a file transfer, or a film
   *   somebody is watching. Only for a setting that genuinely needs it.
   */
  apply?: 'reload' | 'nmbd' | 'all';
  config?: string;
}

interface SambaSettingState {
  share: string;
  key: string;
  value: string;
  apply: string;
  config: string;
  /** What Samba resolved this key to, which is the answer rather than the request. */
  effective: string;
}

const APPLY: Record<string, string> = {
  reload: 'smbcontrol all reload-config >/dev/null 2>&1 || true',
  nmbd: 'smbcontrol all reload-config >/dev/null 2>&1; systemctl try-restart nmbd 2>/dev/null || true',
  all: 'systemctl try-restart nmbd 2>/dev/null; systemctl try-restart smbd 2>/dev/null || true',
};

/** What Samba says this one key resolves to, or null when the section is not there. */
export async function readSetting(
  host: Target,
  share: string,
  key: string,
  config = SMB_CONF,
): Promise<string | null> {
  const effective = await readShare(host, share, config);
  if (!effective) return null;
  // testparm lowercases nothing but pads everything, and a key may be written with any alignment
  const found = Object.entries(effective).find(([name]) => sameKey(name, key));
  return found?.[1] ?? null;
}

/**
 * The command that asks Samba what it resolved.
 *
 * Its own function because **the flag is the bug**. `-s` suppresses the prompt and prints only what
 * differs from the defaults; `-v` is what makes a setting equal to its default visible at all. A
 * test against a fixture cannot notice that flag being wrong — the fixture is whatever output was
 * pasted into it — so the flag is pinned here instead, where a test can read it.
 */
export function testparmCommand(config = SMB_CONF): string {
  return `testparm -sv ${shellQuote(config)} 2>/dev/null`;
}

/**
 * Only the keys somebody asked about.
 *
 * `testparm -v` answers with every parameter Samba has, which is what makes a default visible and
 * would also put a few hundred keys into the state file — noise in every diff, for values nobody
 * declared. What is worth storing is Samba's answer to the questions this resource asked.
 */
export function narrowTo(effective: Record<string, string>, keys: string[]): Record<string, string> {
  const kept: Record<string, string> = {};
  for (const key of keys) {
    const found = Object.entries(effective).find(([name]) => sameKey(name, key));
    if (found) kept[found[0]] = found[1];
  }
  return kept;
}

/** `disagreeing`, but through Samba's vocabulary rather than by string equality. */
function overriddenIn(declared: Record<string, string>, effective: Record<string, string>): string[] {
  return Object.entries(declared)
    .filter(([key, value]) => {
      const answered = Object.entries(effective).find(([name]) => sameKey(name, key))?.[1];
      return answered !== undefined && !sambaSameValue(key, value, answered);
    })
    .map(([key]) => key)
    .sort();
}

function settingProviderFor(host: Target): pulumi.dynamic.ResourceProvider<SambaSettingArgs, SambaSettingState> {
  const settle = async (args: SambaSettingArgs): Promise<SambaSettingState> => {
    const config = args.config ?? SMB_CONF;
    const how = args.apply ?? 'reload';
    const current = await must(host, escalate(host, `cat ${shellQuote(config)}`));
    const updated = upsertSetting(current, args.share, args.key, args.value);

    if (updated !== current) {
      await apply(host, updated, config);
      await must(host, escalate(host, APPLY[how] ?? APPLY.reload ?? 'true'));
    }

    const effective = await readSetting(host, args.share, args.key, config);
    return { share: args.share, key: args.key, value: args.value, apply: how, config, effective: effective ?? '' };
  };

  return {
    async create(args) {
      return { id: `${args.share}#${args.key}`, outs: await settle(args) };
    },

    async read(id, state) {
      const [share = '', key = ''] = id.split('#');
      const config = state?.config ?? SMB_CONF;
      const effective = await readSetting(host, share, key, config);
      // the section is gone entirely: there is nothing here to describe any more
      if (effective === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          value: state?.value ?? effective,
          apply: state?.apply ?? 'reload',
          config,
          ...state,
          share,
          key,
          effective,
        },
      };
    },

    async update(id, _old, args) {
      const [share = '', key = ''] = id.split('#');
      return { outs: await settle({ ...args, share, key }) };
    },

    async diff(_id, old, args) {
      return {
        // compared against what Samba resolved, so a hand edit is drift
        changes: transportChanged(old)
          // through Samba's vocabulary, not verbatim: `netbios name` comes back uppercased, so a
          // declared `homelab` against an effective `HOMELAB` was drift on every run
          || !sambaSameValue(args.key, args.value, old.effective ?? '')
          || old.value !== args.value
          || old.apply !== (args.apply ?? 'reload'),
        replaces: old.share !== args.share || old.key !== args.key ? ['share', 'key'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const [share = '', key = ''] = id.split('#');
      const config = state.config ?? SMB_CONF;
      const current = await must(host, escalate(host, `cat ${shellQuote(config)}`));
      // the key goes and the section stays: this resource never owned the rest of it
      await apply(host, removeSetting(current, share, key), config);
    },
  };
}

/** One setting in one section, for a section nobody should own outright. */
export class SambaSetting extends pulumi.dynamic.Resource {
  declare readonly key: pulumi.Output<string>;
  declare readonly effective: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SambaSettingArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(settingProviderFor(host)), name, { apply: 'reload', config: SMB_CONF, effective: undefined, ...args },
      withLegacyAlias(opts), 'homelab', 'SambaSetting');
  }
}

/** A Samba account, whose existence can be read and whose password cannot. */
export class SambaUser extends pulumi.dynamic.Resource {
  declare readonly name: pulumi.Output<string>;
  declare readonly exists: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: SambaUserArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(userProviderFor(host)), name, { exists: undefined, ...args }, withLegacyAlias({
      // the password is in the state file whatever happens; marking it means it is at least masked
      // in previews and in the console rather than printed to whoever is watching a deployment
      additionalSecretOutputs: ['password'],
      ...opts,
    }), 'homelab', 'SambaUser');
  }
}
