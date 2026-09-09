import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';

/**
 * How much of the journal the machine keeps, and where.
 *
 * The setting that matters on a machine booting off an SD card is `Storage`: `volatile` keeps the
 * journal in RAM and writes nothing, which is the difference between a card that lasts years and
 * one that does not. The rest are limits on how much is kept before the oldest is dropped.
 *
 * It writes a **drop-in** under `/etc/systemd/journald.conf.d/` rather than editing
 * `journald.conf`. The distribution owns that file, and a resource that edits it argues with every
 * upgrade — the same reasoning that keeps `SystemdUnit` out of `/lib/systemd/system`.
 *
 * The `read` answers two different questions on purpose, because the file this resource wrote is
 * not the last word on what journald does. A drop-in that sorts later wins, so:
 *
 * - `content` is what **this resource** put there, and is what `diff` compares. It is the only part
 *   this resource can honestly claim to control.
 * - `effective` is what journald **actually ends up with**, parsed from
 *   `systemd-analyze cat-config`, which is systemd showing its own merged view rather than us
 *   guessing at the merge.
 * - `overridden` names any setting where those two disagree — another file further down the sort
 *   order winning. That is not drift this resource should fix by rewriting its own file, because it
 *   would lose the same argument again on the next run. It is something a person has to see.
 *
 * Two things are reported and deliberately not managed. `/var/log/journal` is not removed when
 * `Storage=volatile` is set — journald simply stops writing to it, and the directory sits there
 * still holding whatever it held, which on a small card is exactly the space somebody was trying to
 * reclaim. Deleting logs is not something a deployment should decide to do, so it is reported and
 * left alone.
 */
export interface JournaldArgs {
  /**
   * Settings for the `[Journal]` section, spelled exactly as journald spells them:
   * `{ Storage: 'volatile', SystemMaxUse: '200M', MaxRetentionSec: '7day' }`.
   *
   * A map rather than named arguments because that is genuinely the shape of the data — journald's
   * configuration is key and value, and a resource offering four of them by name would need a new
   * release the first time somebody wanted a fifth. The cost is that a misspelled key is accepted
   * by both this resource and journald, which is what `overridden` and `effective` are for: a key
   * that never appears in the effective configuration was never understood.
   */
  settings: Record<string, string>;
  /**
   * The drop-in's name, without `.conf`. Defaults to `90-` and the resource's own name.
   *
   * The prefix is not decoration. Drop-ins are read in filename order and the last assignment wins,
   * so a resource writing `storage.conf` loses to a hand-written `10-volatile.conf` that was there
   * first — and loses *silently*, reporting its own file as correct while journald does something
   * else. That happened on a real machine within an hour of this resource existing. `90-` wins
   * against the conventional `10-` and `50-` without claiming the very end of the range.
   */
  file?: string;
  /** Which drop-in directory, for a machine that does not keep it at `/etc/systemd/journald.conf.d`. */
  directory?: string;
  /** Which directory decides whether the journal persists. `Storage=auto` writes there if it exists. */
  journal?: string;
}

interface JournaldState {
  file: string;
  content: string;
  /** What journald actually ends up with, across every drop-in and the distribution's own file. */
  effective: Record<string, string>;
  /** Settings this resource asked for that something else wins. */
  overridden: string[];
  /** Whether `/var/log/journal` exists — reported, never touched. */
  persistent: boolean;
  /**
   * What `/var/log/journal` actually is: `absent`, `directory`, or the target of the symlink.
   *
   * The field that stops `Storage=auto` reading as correct on a machine where auto resolves to the
   * SD card. `auto` means "persist if the directory exists", so the setting alone says nothing
   * about *where* the journal lands — a real directory and a symlink onto an array are the same
   * configuration and completely different machines. Reported, never managed: the link belongs to
   * `Symlink`, and two resources writing one path is the argument this whole design avoids.
   */
  journalDir: string;
  directory: string;
  journal: string;
}

const DIRECTORY = '/etc/systemd/journald.conf.d';
const PERSISTENT = '/var/log/journal';

const pathOf = (file: string, directory = DIRECTORY) => `${directory}/${file}.conf`;

/** The drop-in, as it should appear on disk. */
export function journaldFile(settings: Record<string, string>): string {
  const lines = Object.entries(settings).map(([key, value]) => `${key}=${value}`);
  return `# Managed by Pulumi. Hand edits show up as drift on the next \`pulumi up --refresh\`.\n[Journal]\n${lines.join('\n')}\n`;
}

/**
 * The merged configuration, out of what `systemd-analyze cat-config` prints.
 *
 * It prints every file that contributes, in the order systemd reads them, comments and all. Later
 * assignments win, which is systemd's own rule and the reason this parses the whole output rather
 * than stopping at the first hit — the first hit is usually the distribution's default, commented
 * out or not, and the answer is at the bottom.
 */
export function parseEffective(out: string): Record<string, string> {
  const effective: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const text = line.trim();
    // `#Storage=auto` is how the shipped file documents a default rather than setting one, and
    // reading those as settings would report values nothing had chosen
    if (text.length === 0 || text.startsWith('#') || text.startsWith(';') || text.startsWith('[')) continue;
    const equals = text.indexOf('=');
    if (equals <= 0) continue;
    effective[text.slice(0, equals).trim()] = text.slice(equals + 1).trim();
  }
  return effective;
}

/** Which of the settings asked for are not what journald ends up with. */
export function overriddenBy(settings: Record<string, string>, effective: Record<string, string>): string[] {
  return Object.entries(settings)
    .filter(([key, value]) => effective[key] !== value)
    .map(([key]) => key)
    .sort();
}

/** What the machine says about the drop-in and about journald as a whole, or null where the file is gone. */
export async function readJournald(
  host: Target,
  file: string,
  settings: Record<string, string>,
  directory = DIRECTORY,
  journal = PERSISTENT,
): Promise<JournaldState | null> {
  const path = pathOf(file, directory);
  const asked = await ask(host, escalate(host,
    `test -f ${shellQuote(path)} || exit 9; ` +
    // markers, so one round trip answers all three. The file first, since it is the only part with
    // a newline count nobody can predict
    `cat ${shellQuote(path)}; echo '#pulumi-homelab#effective'; ` +
    `systemd-analyze cat-config systemd/journald.conf 2>/dev/null; ` +
    `echo '#pulumi-homelab#persistent'; ` +
    // what it *is*, not merely whether it is there: readlink first, so a symlink reports its target
    // rather than being flattened into 'directory' by a test that follows it
    `if pointsAt=$(readlink ${shellQuote(journal)} 2>/dev/null); then printf '%s' "$pointsAt"; ` +
    `elif [ -d ${shellQuote(journal)} ]; then echo directory; else echo absent; fi`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read journald configuration: ${asked.err.trim()}`);

  const [ours = '', rest = ''] = splitOn(asked.out, '#pulumi-homelab#effective');
  const [merged = '', reported = ''] = splitOn(rest, '#pulumi-homelab#persistent');
  const effective = parseEffective(merged);
  const journalDir = reported.trim();
  return {
    file,
    content: ours,
    effective,
    overridden: overriddenBy(settings, effective),
    persistent: journalDir !== 'absent',
    journalDir,
    directory,
    journal,
  };
}

/** Split once on a marker line, keeping everything either side of it. */
function splitOn(text: string, marker: string): [string, string] {
  const at = text.indexOf(`${marker}\n`);
  if (at < 0) return [text, ''];
  return [text.slice(0, at), text.slice(at + marker.length + 1)];
}

async function apply(host: Target, file: string, content: string, directory = DIRECTORY): Promise<void> {
  await must(host, escalate(host,
    `mkdir -p ${shellQuote(directory)} && ` +
    `${heredoc(pathOf(file, directory), content)}\n` +
    `chmod 0644 ${shellQuote(pathOf(file, directory))} && ` +
    // restarting journald is safe and keeps this boot's logs: the socket holds what arrives while
    // it is down, which is why this is a restart rather than the reboot the manual page implies
    `systemctl restart systemd-journald`,
  ));
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<JournaldArgs, JournaldState> {
  const settle = async (file: string, args: JournaldArgs): Promise<JournaldState> => {
    const directory = args.directory ?? DIRECTORY;
    const journal = args.journal ?? PERSISTENT;
    const content = journaldFile(args.settings);
    // an update that changes nothing must do nothing: every diff here reports a change when the
    // serialised provider differs, so without this a comment edited in this package would restart
    // journald on every machine it manages
    const before = await readJournald(host, file, args.settings, directory, journal);
    if (!before || before.content !== content) await apply(host, file, content, directory);
    const actual = await readJournald(host, file, args.settings, directory, journal);
    if (!actual) throw new Error(`wrote ${pathOf(file, directory)} but it is not there`);
    return actual;
  };

  return {
    async create(args) {
      const file = args.file ?? '';  // the class always supplies it
      return { id: file, outs: await settle(file, args) };
    },

    async read(id, state) {
      const actual = await readJournald(
        host, id, parseSettings(state?.content ?? ''),
        state?.directory ?? DIRECTORY, state?.journal ?? PERSISTENT,
      );
      if (!actual) return { id: undefined, props: undefined };
      return { id, props: { ...state, ...actual } };
    },

    async update(id, _old, args) {
      return { outs: await settle(id, args) };
    },

    async diff(_id, old, args) {
      const content = journaldFile(args.settings);
      const file = args.file ?? old.file;
      return {
        // only our own file is compared. `effective` and `overridden` are what the machine decided
        // and are reported rather than reconciled — rewriting our drop-in to win an argument with a
        // file that sorts after it would lose the same argument again on the next run
        changes: transportChanged(old)
          || old.content !== content || old.file !== file,
        replaces: old.file !== file ? ['file'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id) {
      // the drop-in goes; the journal directory and its contents stay, because deleting logs is not
      // a decision a deployment gets to make
      await must(host, escalate(host, `rm -f ${shellQuote(pathOf(id))} && systemctl restart systemd-journald`));
    },
  };
}

/** The settings out of a drop-in this resource wrote, for a refresh that has only the file to go on. */
export function parseSettings(content: string): Record<string, string> {
  return parseEffective(content);
}

/** How much journal the machine keeps, checked against systemd's own merged view. */
export class Journald extends pulumi.dynamic.Resource {
  declare readonly effective: pulumi.Output<Record<string, string>>;
  declare readonly overridden: pulumi.Output<string[]>;
  declare readonly persistent: pulumi.Output<boolean>;
  declare readonly journalDir: pulumi.Output<string>;

  constructor(name: string, host: Target, args: JournaldArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      effective: undefined,
      overridden: undefined,
      persistent: undefined,
      journalDir: undefined,
      directory: DIRECTORY,
      journal: PERSISTENT,
      ...args,
      file: args.file ?? `90-${name}`,
    }, withLegacyAlias(opts), 'homelab', 'Journald');
  }
}
