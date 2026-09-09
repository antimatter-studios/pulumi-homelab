import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredocInto, must, shellQuote, type Target, describe } from '../ssh.ts';
import { stamped, transportChanged, withLegacyAlias } from '../upgrade.ts';
import { disagreeing } from '../resolved.ts';

/**
 * A drop-in for sshd, never an edit to `sshd_config`.
 *
 * Three things this gets right that a `ManagedFile` would not, in the order they bite:
 *
 * 1. **It validates before installing.** `sshd -t -f <candidate>` on a temporary file, the same
 *    shape as `SudoRule`'s `visudo -cqf`. An `sshd_config` that does not parse means sshd will not
 *    start, and on a machine reached only over ssh that is a keyboard-and-monitor recovery.
 * 2. **It checks the `Include` exists.** A drop-in is only read if `sshd_config` contains
 *    `Include /etc/ssh/sshd_config.d/*.conf`, and older or hand-edited files often do not. Without
 *    it the file is written, looks correct, and does nothing — the same shape as a `config.txt`
 *    setting under a filter that never matches. A hard failure naming the missing line, not a
 *    warning.
 * 3. **It reads the effective configuration with `sshd -T`**, not the file it wrote. Reading back
 *    your own file is reading your own homework: `sshd -T` is sshd resolving every drop-in, every
 *    default and every `Match` block, which is the only thing that answers what will happen.
 *
 * `reload`, never `restart` — `systemctl reload ssh` re-reads the configuration without dropping
 * existing connections, and one of those connections is the one applying the change.
 *
 * **The honest limit, which no resource can close:** `sshd -t` proves the file parses, not that
 * anybody can still log in. `PermitRootLogin no` on a machine whose only account is root parses
 * perfectly and locks it. That is the argument for `match` being reached for early rather than
 * late — a change scoped to a subnet you are already on is a change you can undo.
 */
export interface SshdSettings {
  passwordAuthentication?: boolean;
  pubkeyAuthentication?: boolean;
  kbdInteractiveAuthentication?: boolean;
  permitEmptyPasswords?: boolean;
  x11Forwarding?: boolean;
  allowAgentForwarding?: boolean;
  printMotd?: boolean;
  usePAM?: boolean;
  strictModes?: boolean;
  ignoreRhosts?: boolean;
  tcpKeepAlive?: boolean;
  useDNS?: boolean;

  permitRootLogin?: 'no' | 'prohibit-password' | 'forced-commands-only' | 'yes';
  allowTcpForwarding?: 'yes' | 'no' | 'local' | 'remote';
  gatewayPorts?: 'no' | 'yes' | 'clientspecified';
  logLevel?: 'QUIET' | 'FATAL' | 'ERROR' | 'INFO' | 'VERBOSE' | 'DEBUG' | 'DEBUG1' | 'DEBUG2' | 'DEBUG3';
  compression?: 'yes' | 'no' | 'delayed';

  port?: number;
  maxAuthTries?: number;
  maxSessions?: number;
  loginGraceTime?: number;
  clientAliveInterval?: number;
  clientAliveCountMax?: number;

  allowUsers?: string[];
  allowGroups?: string[];
  denyUsers?: string[];
  denyGroups?: string[];
  listenAddress?: string[];
  ciphers?: string[];
  macs?: string[];
  kexAlgorithms?: string[];

  /** `10:30:100` is three numbers in a trench coat, so it is a string and honest about it. */
  maxStartups?: string;
}

/** The criteria a `Match` block can be scoped by. */
export interface SshdMatch {
  Address?: string;
  User?: string;
  Group?: string;
  Host?: string;
  LocalAddress?: string;
  LocalPort?: string;
}

export interface SshdConfigArgs {
  /** The drop-in's name, without `.conf`. Defaults to `90-` and the resource's own name. */
  file?: string;
  settings?: SshdSettings;
  /**
   * Anything `SshdSettings` does not name.
   *
   * sshd has around ninety keywords and the long tail is real — `AuthorizedKeysCommand`,
   * `ChrootDirectory`, `PerSourceMaxStartups`, `RekeyLimit`. Spelled `unchecked` so that reaching
   * for it reads as a decision, as it does in `BootConfig`.
   */
  unchecked?: Record<string, string>;
  /**
   * Scope these settings to a `Match` block.
   *
   * The thing that makes a dangerous change survivable: `PasswordAuthentication yes` inside
   * `Match Address 10.0.0.0/24` enables password login on a trusted network while leaving anything
   * else key-only.
   */
  match?: SshdMatch;
  /** Which drop-in directory, and which file the `Include` must be in. */
  directory?: string;
  config?: string;
}

interface SshdConfigState {
  file: string;
  settings: Record<string, string>;
  match: SshdMatch;
  directory: string;
  config: string;
  /** What `sshd -T` resolved, normalised — the answer, as against the file's request. */
  effective: Record<string, string>;
  /**
   * Declared keywords sshd does not agree with: something else won.
   *
   * A drop-in that sorts later, a `Match` block, or a keyword already set in `sshd_config` itself.
   * Reported and never reconciled — rewriting this file to win would lose the same argument on the
   * next run.
   */
  overridden: string[];
}

const DIRECTORY = '/etc/ssh/sshd_config.d';
const CONFIG = '/etc/ssh/sshd_config';

const pathOf = (file: string, directory = DIRECTORY) => `${directory}/${file}.conf`;

/** The keyword sshd spells each typed property with. */
const KEYWORDS: Record<string, string> = {
  usePAM: 'UsePAM',
  useDNS: 'UseDNS',
  x11Forwarding: 'X11Forwarding',
  macs: 'MACs',
  kexAlgorithms: 'KexAlgorithms',
};

/** `passwordAuthentication` → `PasswordAuthentication`, for everything not in the table above. */
export function keywordFor(property: string): string {
  return KEYWORDS[property] ?? property.charAt(0).toUpperCase() + property.slice(1);
}

/**
 * The typed settings as sshd's own keywords and values.
 *
 * Booleans become `yes` and `no`, and that one matters more here than in `BootConfig`: sshd
 * *rejects* `false` rather than ignoring it, so a boolean rendered wrong fails at start — on a
 * machine reached only by ssh, the expensive kind of loud. Lists are comma-separated, which is what
 * every list-valued keyword takes.
 */
export function renderSshd(settings: SshdSettings, unchecked: Record<string, string> = {}): Record<string, string> {
  const rendered: Record<string, string> = {};
  for (const [property, value] of Object.entries(settings)) {
    if (value === undefined) continue;
    const keyword = keywordFor(property);
    if (typeof value === 'boolean') rendered[keyword] = value ? 'yes' : 'no';
    else if (Array.isArray(value)) rendered[keyword] = value.join(',');
    else rendered[keyword] = String(value);
  }
  return { ...rendered, ...unchecked };
}

/**
 * What `sshd -T` says, put into the same alphabet as what gets written.
 *
 * **`sshd -T` does not print what you write**, and comparing the two directly is permanent false
 * drift on a machine nobody has touched — the same family as `stat` answering `644` where the code
 * says `0644`, and rclone's obscure never returning the same string twice.
 *
 * Three differences, all real, all observed on one machine:
 *
 * - every keyword comes back **lowercased**, so `PasswordAuthentication` is
 *   `passwordauthentication`;
 * - `PermitRootLogin prohibit-password` comes back as **`without-password`** — the deprecated
 *   spelling of the same thing, and the one sshd prints. A resource comparing the modern spelling
 *   against it reports drift for ever and "corrects" it on every deployment;
 * - values are sshd's own vocabulary: `yes`/`no` rather than anything TypeScript-shaped.
 *
 * The keys come back lowercased on purpose rather than being mapped to their real spellings: a
 * comparison needs both sides in one alphabet, and lowercase is the one sshd chose.
 */
export function parseSshdT(out: string): Record<string, string> {
  const effective: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const text = line.trim();
    if (text.length === 0) continue;
    const space = text.indexOf(' ');
    if (space <= 0) continue;
    effective[text.slice(0, space).toLowerCase()] = normaliseSshdValue(text.slice(space + 1).trim());
  }
  return effective;
}

/** `without-password` and `prohibit-password` are the same answer; sshd prints the older one. */
export function normaliseSshdValue(value: string): string {
  return value === 'without-password' ? 'prohibit-password' : value;
}

/** Whether `sshd_config` actually reads the drop-in directory. */
export function hasInclude(text: string, directory = DIRECTORY): boolean {
  return text.split('\n').some((line) => {
    const words = line.trim().split(/\s+/);
    return words[0]?.toLowerCase() === 'include'
      && (words[1] ?? '').startsWith(directory);
  });
}

/** The drop-in, with its settings inside a `Match` block when there is one. */
export function sshdDropIn(settings: Record<string, string>, match: SshdMatch = {}): string {
  const lines = Object.entries(settings).map(([keyword, value]) => `${keyword} ${value}`);
  const criteria = Object.entries(match).filter(([, value]) => value !== undefined);
  const head = '# Managed by Pulumi. Hand edits show up as drift on the next `pulumi up --refresh`.';
  if (criteria.length === 0) return `${head}\n${lines.join('\n')}\n`;
  // a Match block runs to the next Match or the end of the file, so everything after it is scoped —
  // which is why this resource writes its own file rather than appending to somebody else's
  const scope = criteria.map(([key, value]) => `${key} ${value}`).join(' ');
  return `${head}\nMatch ${scope}\n${lines.map((line) => `    ${line}`).join('\n')}\n`;
}

/** The file, whether the Include is there, and what sshd resolved. */
export async function readSshdConfig(
  host: Target,
  file: string,
  directory = DIRECTORY,
  config = CONFIG,
): Promise<{ content: string | null; included: boolean; effective: Record<string, string> }> {
  const asked = await ask(host, escalate(host,
    `cat ${shellQuote(config)} 2>/dev/null || true; echo '#pulumi-homelab#drop'; ` +
    `cat ${shellQuote(pathOf(file, directory))} 2>/dev/null || true; echo '#pulumi-homelab#effective'; ` +
    // -T needs a full parse, so it fails on a machine whose config is already broken; that is an
    // answer worth having rather than an empty one
    `sshd -T 2>/dev/null || true`,
  ));
  if (asked.code !== 0) throw new Error(`could not read the sshd configuration: ${asked.err.trim()}`);

  const [main = '', rest = ''] = asked.out.split('#pulumi-homelab#drop\n');
  const [dropIn = '', resolved = ''] = rest.split('#pulumi-homelab#effective\n');
  return {
    content: dropIn.trim().length > 0 ? dropIn : null,
    included: hasInclude(main, directory),
    effective: parseSshdT(resolved),
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<SshdConfigArgs, SshdConfigState> {
  const settle = async (file: string, args: SshdConfigArgs): Promise<SshdConfigState> => {
    const directory = args.directory ?? DIRECTORY;
    const config = args.config ?? CONFIG;
    const settings = renderSshd(args.settings ?? {}, args.unchecked ?? {});
    const match = args.match ?? {};
    const content = sshdDropIn(settings, match);

    const before = await readSshdConfig(host, file, directory, config);
    if (!before.included) {
      throw new Error(
        `${config} on ${describe(host)} does not include ${directory}, so a drop-in written there ` +
        `would be read by nothing. Add \`Include ${directory}/*.conf\` to ${config} first — a file ` +
        `that is never read looks exactly like a setting that was applied and did not take effect.`,
      );
    }

    if (before.content !== content) {
      await must(host, escalate(host,
        `mkdir -p ${shellQuote(directory)} && staging=$(mktemp) && ` +
        `${heredocInto('"$staging"', content)}\n` +
        // validated before it is installed: an sshd_config that does not parse stops sshd starting,
        // and the machine is reached only through sshd
        `sshd -t -f "$staging" || { rm -f "$staging"; echo "the resulting sshd configuration does not parse" >&2; exit 1; }; ` +
        `install -m 0644 -o root -g root "$staging" ${shellQuote(pathOf(file, directory))} && rm -f "$staging" && ` +
        // reload, never restart: a restart drops every connection including the one applying this
        `systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true`,
      ));
    }

    const actual = await readSshdConfig(host, file, directory, config);
    return {
      file, settings, match, directory, config,
      effective: actual.effective,
      // sshd -T lowercases every keyword, so the declared name has to be asked for in its alphabet
      // or it looks absent and is never compared at all
      overridden: disagreeing(settings, actual.effective, (keyword) => keyword.toLowerCase()),
    };
  };

  return {
    async create(args) {
      const file = args.file ?? '';
      return { id: file, outs: await settle(file, args) };
    },

    async read(id, state) {
      const directory = state?.directory ?? DIRECTORY;
      const config = state?.config ?? CONFIG;
      const actual = await readSshdConfig(host, id, directory, config);
      // the drop-in is gone: Pulumi forgets it and the next up puts it back
      if (actual.content === null) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          settings: state?.settings ?? {},
          match: state?.match ?? {},
          directory,
          config,
          ...state,
          file: id,
          effective: actual.effective,
          overridden: disagreeing(
            state?.settings ?? {}, actual.effective, (keyword) => keyword.toLowerCase(),
          ),
        },
      };
    },

    async update(id, _old, args) {
      return { outs: await settle(id, args) };
    },

    async diff(_id, old, args) {
      const settings = renderSshd(args.settings ?? {}, args.unchecked ?? {});
      const match = args.match ?? {};
      return {
        changes: transportChanged(old)
          || JSON.stringify(old.settings) !== JSON.stringify(settings)
          || JSON.stringify(old.match) !== JSON.stringify(match)
          || old.file !== (args.file ?? old.file),
        replaces: old.file !== (args.file ?? old.file) ? ['file'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      await must(host, escalate(host,
        `rm -f ${shellQuote(pathOf(id, state.directory ?? DIRECTORY))} && ` +
        `systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || true`,
      ));
    },
  };
}

/** Settings for sshd, written as a drop-in and read back from sshd's own resolution of them. */
export class SshdConfig extends pulumi.dynamic.Resource {
  declare readonly file: pulumi.Output<string>;
  declare readonly effective: pulumi.Output<Record<string, string>>;

  constructor(name: string, host: Target, args: SshdConfigArgs, opts?: pulumi.CustomResourceOptions) {
    super(stamped(providerFor(host)), name, {
      settings: {},
      unchecked: {},
      match: {},
      directory: DIRECTORY,
      config: CONFIG,
      effective: undefined,
      overridden: undefined,
      ...args,
      file: args.file ?? `90-${name}`,
    }, withLegacyAlias(opts), 'homelab', 'SshdConfig');
  }
}
