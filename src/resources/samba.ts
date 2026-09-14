import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
import { disagreeing } from '../resolved.ts';

/**
 * Samba shares and users, read back from the file, with `testparm` asked whether it parses.
 *
 * **The two jobs are separate, and giving both to `testparm` was a bug.** It prints the effective
 * configuration as smbd resolves it, which sounds like the better read and is not one at all,
 * because it does not answer in the alphabet the declaration is written in. Measured against two
 * real shares of twenty-five settings each, it reported thirteen, for four independent reasons:
 *
 * - **a share setting that matches `[global]` is not repeated**, so it is simply absent;
 * - **a value equal to Samba's default is omitted**, which `-v` fixes and nothing else does;
 * - **spellings are normalised** — `2775` comes back `02775`, `no` comes back `No`;
 * - **synonyms are collapsed** — `writeable = yes` *is* `read only = no`, and only the canonical
 *   one is printed, so the declared one never matches anything.
 *
 * Closing that gap semantically would mean carrying Samba's synonym table, its per-version default
 * table, and enough of its resolution order to tell "absent because it matches `[global]`" from
 * "absent because it is the default" — a reimplementation of Samba's configuration semantics
 * inside a Pulumi resource, wrong in a new way every time Samba changed.
 *
 * So the read is the **section in the file**, which is what this resource wrote and round-trips
 * exactly, and `testparm` answers the question only it can: does what is on the machine parse.
 * That is a real question — a broken `smb.conf` does not break the share it is in, it stops smbd
 * reloading, and the symptom is a share that quietly does not exist.
 *
 * What is genuinely given up is "is this setting in force", since a share setting can be overridden
 * above it. That is a question about Samba's resolution order rather than about whether the machine
 * matches the declaration, and the resource that answered it would have to model everything above.
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
  /** The section as the file says it, which is the read and the thing compared. */
  actual: Record<string, string>;
  /**
   * Whether Samba can parse the configuration on the machine.
   *
   * The job `testparm` is kept for, and the one only it can do. A configuration that will not parse
   * does not break the share it is in — it stops smbd reloading, so the share quietly does not
   * exist, and nothing about the file itself looks wrong.
   */
  parses: boolean;
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

/** One section of `testparm` output, as Samba understands it. Reported, never compared. */
export function effectiveShare(testparm: string, share: string): Record<string, string> | null {
  const lines = parseSections(testparm).get(share);
  return lines ? parseShareSettings(lines) : null;
}

/**
 * One section of `smb.conf` itself — the read this resource compares against.
 *
 * The same parse as `effectiveShare` and a different source, which is the whole point: this is what
 * the resource wrote, so it comes back in the alphabet it was written in and a hand edit shows up
 * as itself rather than as Samba's resolution of it.
 */
export function fileShare(text: string, share: string): Record<string, string> | null {
  const lines = parseSections(text).get(share);
  return lines ? parseShareSettings(lines) : null;
}

/**
 * Whether the file says something other than what was declared.
 *
 * Through `sambaSameValue` rather than by string equality, even though the file is what this wrote:
 * somebody who hand-edits `read only = no` to `read only = No` has changed nothing, and rewriting
 * the file to correct a capital letter is the same noise this resource was built to stop making.
 *
 * Extra keys in the section are not drift. A share is edited in place, so a setting somebody added
 * by hand is theirs — the same bargain `upsertSetting` makes with `[global]`.
 */
export function shareDiffers(actual: Record<string, string>, wanted: Record<string, string>): boolean {
  return Object.entries(wanted).some(([key, value]) => {
    const found = Object.entries(actual).find(([name]) => sameKey(name, key))?.[1];
    return found === undefined || !sambaSameValue(key, value, found);
  });
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

const PARSES_MARKER = '#pulumi-homelab#parses';

/** The file, and whether Samba can parse it, split apart again. */
export function parseShareRead(out: string, share: string): { actual: Record<string, string> | null; parses: boolean } {
  const [text = '', verdict = ''] = out.split(`${PARSES_MARKER}\n`);
  return { actual: fileShare(text, share), parses: verdict.trim() === 'ok' };
}

/**
 * The section as the file has it, and Samba's verdict on the whole configuration.
 *
 * One round trip for both, because they are two questions about the same file and the second is an
 * exit code. `testparm`'s output is discarded here — it is being asked whether, not what.
 */
export async function readShareSection(
  host: Target,
  share: string,
  config = SMB_CONF,
): Promise<{ actual: Record<string, string> | null; parses: boolean } | null> {
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(config)} || exit 9; cat ${shellQuote(config)}; echo '${PARSES_MARKER}'; `
    + `testparm -s ${shellQuote(config)} >/dev/null 2>&1 && echo ok || true`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read ${config}: ${asked.err.trim()}`);
  return parseShareRead(asked.out, share);
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

    const after = await readShareSection(host, args.share, config);
    if (after === null || after.actual === null) {
      throw new Error(`wrote the [${args.share}] section to ${config} but it is not there`);
    }
    if (shareDiffers(after.actual, { path: args.path, ...settings })) {
      throw new Error(
        `wrote the [${args.share}] section to ${config} on ${describe(host)} and it does not read `
        + `back as declared — something else is editing the same file`,
      );
    }
    return { share: args.share, path: args.path, settings, config, actual: after.actual, parses: after.parses };
  };

  return {
    async create(args) {
      return { id: args.share, outs: await settle(args) };
    },

    async read(id, state) {
      const config = state?.config ?? SMB_CONF;
      const found = await readShareSection(host, id, config);
      // no file, or no section in it: Pulumi forgets it and the next up writes it back
      if (found === null || found.actual === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          settings: state?.settings ?? {},
          config,
          ...state,
          share: id,
          // what the file says the path is, which is the answer that matters when they disagree
          path: found.actual.path ?? state?.path ?? '',
          // the two that always come from the machine rather than from what was remembered
          actual: found.actual,
          parses: found.parses,
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle({ ...args, share: id }) };
    },

    async diff(_id, old, args) {
      const settings = args.settings ?? {};
      // compared against the section in the file rather than against the last arguments, so a hand
      // edit is drift — and against the file rather than against testparm, so a share Samba
      // reports in its own vocabulary is not drift on every single deployment for ever
      const wanted = { path: args.path, ...settings };
      return {
        changes: transportChanged(old)
          || shareDiffers(old.actual ?? {}, wanted)
          // a configuration Samba cannot parse is a share that quietly does not exist, however
          // right the file looks. Rewriting is the only move available, and apply refuses to
          // install anything that still does not parse
          || old.parses === false
          || old.share !== args.share,
        replaces: old.share !== args.share ? ['share'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      const config = state.config ?? SMB_CONF;
      const current = await must(host, escalate(host, `cat ${shellQuote(config)}`));
      await apply(host, removeSection(current, id), config);
    },
  };
}

/** A Samba share, checked against the file it was written to, with `testparm` asked only whether it parses. */
export class SambaShare extends pulumi.dynamic.Resource {
  declare readonly share: pulumi.Output<string>;
  /** The section as the file has it. */
  declare readonly actual: pulumi.Output<Record<string, string>>;
  /** Whether Samba can parse the configuration at all. */
  declare readonly parses: pulumi.Output<boolean>;

  constructor(name: string, host: Target, args: SambaShareArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      actual: undefined, parses: undefined, settings: {}, config: SMB_CONF, ...args,
    }, withLegacyAlias(opts), 'homelab', 'SambaShare');
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

/** One `key = value` out of one section of the file, or null where the section has no such key. */
export function fileSetting(text: string, share: string, key: string): string | null {
  const section = fileShare(text, share);
  if (section === null) return null;
  return Object.entries(section).find(([name]) => sameKey(name, key))?.[1] ?? null;
}

interface SambaSettingState {
  share: string;
  key: string;
  value: string;
  apply: string;
  config: string;
  /** What the line in the file says — the read, and the thing compared. */
  actual: string;
  /**
   * What Samba resolved this key to.
   *
   * Reported and never compared. It is the answer to "what did this turn into", which is worth
   * having, and it is not in the alphabet the declaration is written in — see the note on
   * `SambaShare`.
   */
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

    const written = await must(host, escalate(host, `cat ${shellQuote(config)}`));
    const actual = fileSetting(written, args.share, args.key);
    if (actual === null || !sambaSameValue(args.key, args.value, actual)) {
      throw new Error(
        `wrote ${args.key} to [${args.share}] in ${config} on ${describe(host)} and it reads back as `
        + `${actual ?? 'nothing'} — something else is editing the same file`,
      );
    }
    // Samba's resolution, kept as information: it is the answer to "what did this turn into", which
    // is worth reporting and is not a thing to compare against — see the note on SambaShare
    const effective = await readSetting(host, args.share, args.key, config);
    return { share: args.share, key: args.key, value: args.value, apply: how, config, actual, effective: effective ?? '' };
  };

  return {
    async create(args) {
      return { id: `${args.share}#${args.key}`, outs: await settle(args) };
    },

    async read(id, state) {
      const [share = '', key = ''] = id.split('#');
      const config = state?.config ?? SMB_CONF;
      const text = await ask(host, escalate(host, `cat ${shellQuote(config)}`));
      const actual = text.code === 0 ? fileSetting(text.out, share, key) : null;
      // the line is gone from the file: there is nothing here to describe any more
      if (actual === null) return { id: undefined, props: undefined };
      const effective = await readSetting(host, share, key, config);
      return {
        id,
        props: {
          value: state?.value ?? actual,
          apply: state?.apply ?? 'reload',
          config,
          ...state,
          share,
          key,
          // the two that always come from the machine rather than from what was remembered
          actual,
          effective: effective ?? '',
        },
      };
    },

    async update(id, _old, args) {
      const [share = '', key = ''] = id.split('#');
      return { outs: await settle({ ...args, share, key }) };
    },

    async diff(_id, old, args) {
      return {
        // compared against the line in the file, so a hand edit is drift — and not against what
        // Samba resolved, which normalises spellings and collapses synonyms and would be drift on
        // every deployment for ever. See the note on SambaShare
        changes: transportChanged(old)
          // still through Samba's vocabulary rather than verbatim, because somebody who hand-edits
          // `no` to `No` has changed nothing and rewriting the file over it is the same noise
          || !sambaSameValue(args.key, args.value, old.actual ?? '')
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
  /** What the file says, which is what drift is measured against. */
  declare readonly actual: pulumi.Output<string>;
  /** What Samba resolved it to. Reported, never compared. */
  declare readonly effective: pulumi.Output<string>;

  constructor(name: string, host: Target, args: SambaSettingArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(settingProviderFor(host)), name, { apply: 'reload', config: SMB_CONF, actual: undefined, effective: undefined, ...args },
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
