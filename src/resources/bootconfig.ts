import * as pulumi from '@pulumi/pulumi';
import { escalate, ask, heredoc, must, shellQuote, type Target, describe } from '../ssh.ts';
import { providerChanged, withLegacyAlias } from '../upgrade.ts';
import { disagreeing } from '../resolved.ts';

/**
 * Lines in a Raspberry Pi's `config.txt`, inside one conditional filter section.
 *
 * **The section is part of the identity, and that is the whole point.** `config.txt` has filter
 * sections — `[all]`, `[pi5]`, `[cm4]`, `[cm5]`, `[none]` and more — and a setting under a section
 * that does not match the board **is not an error**. The firmware reads it, decides it does not
 * apply, and carries on. So a correct-looking line can do nothing at all, and the symptom is not
 * anything mentioning boot configuration: it is a missing drive.
 *
 * That happened on the machine this was written against. `dtparam=pciex1` — which enables the PCIe
 * lane an NVMe array hangs off — sat under `[cm5]`, which matches a Compute Module 5 and never a
 * Pi 5 Model B. Every line looked right. The array simply was not there.
 *
 * A resource keyed only on the setting would have reported it present and been wrong in exactly
 * that way.
 *
 * **Two kinds of line, because the file has two.** Some keys are scalar and unique — `arm_freq`,
 * `gpu_mem` — and setting one twice is a contradiction. `dtoverlay` and `dtparam` legitimately
 * repeat, and *all* of them apply: a key-based upsert would collapse
 * `dtoverlay=vc4-kms-v3d` and `dtoverlay=dwc2,dr_mode=host` into one and silently remove hardware.
 * So `settings` is replaced in place by key, and `overlays` is a set of whole lines that are added
 * if missing and otherwise left alone.
 *
 * **Never regenerated, and never chmod-ed.** Same blast radius as `cmdline.txt` — a wrong
 * `config.txt` is a Pi that does not boot and cannot be fixed over ssh, only by moving the card to
 * another computer. Comments survive because comments are content: a note explaining an afternoon
 * somebody lost, and a commented-out line that is a decision deliberately not taken, are both worth
 * more than the tidiness of rewriting the file. And it lives on a vfat partition, where mode and
 * ownership are meaningless.
 */
/**
 * The options whose value sets are genuinely closed.
 *
 * Typed only where a union is *complete and board-independent*. The asymmetry that decides what
 * belongs here: an unknown **key** is inert, because the firmware ignores what it does not
 * recognise, so `arm_bosot=1` does nothing at all. That undercuts most of the case for an allowlist
 * of names. What is dangerous is a known key with a wrong **value**, and only some of those have a
 * set small enough to write down.
 *
 * The booleans are booleans because that is what they mean; they render as `1` and `0`.
 */
export interface BootSettings {
  arm_64bit?: boolean;
  arm_boost?: boolean;
  disable_overscan?: boolean;
  disable_fw_kms_setup?: boolean;
  disable_splash?: boolean;
  auto_initramfs?: boolean;
  camera_auto_detect?: boolean;
  display_auto_detect?: boolean;
  hdmi_force_hotplug?: boolean;
  otg_mode?: boolean;
  /** 0 auto, 1 CEA, 2 DMT. There is no fourth. */
  hdmi_group?: 0 | 1 | 2;
  hdmi_drive?: 1 | 2;
  /** Quarter turns. */
  display_rotate?: 0 | 1 | 2 | 3;
  max_framebuffers?: 0 | 1 | 2;
  /**
   * `dtparam=audio`, which is closed even though `dtparam` in general is not.
   *
   * It renders as `dtparam=audio=on`, and is matched by the `dtparam=audio=` prefix so that
   * changing it replaces the line rather than adding a second one — which is what would happen if
   * it went through `overlays`, where repetition is the whole point.
   */
  audio?: 'on' | 'off';

  /**
   * Numbers whose valid range depends on the board, and sometimes on each other.
   *
   * `hdmi_mode` means different things under different `hdmi_group` values, and a memory split that
   * is right on one board is wrong on the next. A union would be wrong somewhere and right nowhere
   * in particular, so these are numbers and the operator's judgement.
   */
  gpu_mem?: number;
  hdmi_mode?: number;
  sdram_freq?: number;
  usb_max_current_enable?: boolean;
}

/** `audio` is spelled differently in the file than in the type, and nothing else is. */
const RENDERED_AS: Record<string, string> = { audio: 'dtparam=audio' };

export interface BootConfigArgs {
  /** Which filter section. `all` unless the setting genuinely applies to one board. */
  section?: string;
  /** The typed options, unique by key and replaced in place: `{ arm_freq: 2000 }`. */
  settings?: BootSettings;
  /**
   * Anything `BootSettings` does not name.
   *
   * Named so that reaching for it feels like a decision, because it is one — and the settings that
   * can stop a machine booting are deliberately only reachable this way:
   *
   * - `kernel=` and `initramfs` name a file, and a file that is not there means no boot and no
   *   message;
   * - `arm_freq` and `over_voltage` will not POST, or will run unstably in a way that looks like
   *   failing hardware rather than a configuration mistake.
   *
   * None of those can be validated by a type — the point is not the check, it is that the recovery
   * is a card reader and another computer. `#dtparam=pciex1_gen=3` is the shape of what belongs
   * here too: a real option, deliberately not taken, with a note to leave it until the storage is
   * proven.
   */
  unchecked?: Record<string, string>;
  /**
   * Whole lines that may legitimately appear more than once — `dtoverlay=`, `dtparam=`.
   *
   * Added when missing and never deduplicated against each other, because the firmware applies
   * every one of them and two overlays are two pieces of hardware.
   */
  overlays?: string[];
  /** Where the file is, for an image that keeps it somewhere neither usual place would find. */
  path?: string;
}

interface BootConfigState {
  section: string;
  settings: Record<string, string>;
  overlays: string[];
  path: string;
  /**
   * What the firmware says it actually parsed, for the settings it keeps as config integers.
   *
   * The `/sys/fs/cgroup/cgroup.controllers` of this problem: the file says what was asked for and
   * `vcgencmd get_config` says what the board made of it. It is what would have caught the `[cm5]`
   * mistake on the day it was made rather than whenever somebody noticed the drives were missing.
   *
   * **It does not cover `overlays`.** `dtoverlay` and `dtparam` are consumed by the device tree
   * rather than kept as config integers, so the firmware does not report them here at all, and
   * their effective answer lives in `/proc/device-tree`. Reported as far as it goes rather than
   * pretended at.
   */
  effective: Record<string, string>;
  /**
   * Declared settings the firmware resolved to something else.
   *
   * Only for what `vcgencmd` reports at all — an overlay is not in there, and counting its absence
   * as disagreement would fill this with things nobody can act on.
   */
  overridden: string[];
}

/**
 * The typed settings as the lines they become.
 *
 * Booleans become `1` and `0` rather than `true` and `false`, which is what the firmware reads;
 * a boolean that rendered as `true` would be an unrecognised value on a recognised key, which is
 * the quiet half of the failure this type exists to prevent.
 */
export function renderSettings(settings: BootSettings, unchecked: Record<string, string> = {}): Record<string, string> {
  const rendered: Record<string, string> = {};
  for (const [key, value] of Object.entries(settings)) {
    if (value === undefined) continue;
    rendered[RENDERED_AS[key] ?? key] = typeof value === 'boolean' ? (value ? '1' : '0') : String(value);
  }
  // last, so a deliberate override of a typed key is possible and visible at the call site
  return { ...rendered, ...unchecked };
}

/** Bookworm moved the boot partition; older images have it directly in `/boot`. */
export const BOOT_CANDIDATES = ['/boot/firmware/config.txt', '/boot/config.txt'];

const DEFAULT_SECTION = 'all';

/**
 * Split `config.txt` into its filter sections, keeping every line verbatim.
 *
 * Lines before any `[section]` header belong to an implicit `all`, which is how the firmware reads
 * them — a file that has never been sectioned is entirely `[all]`, and treating those lines as
 * belonging to nothing would make the resource unable to see settings that are plainly in force.
 */
export function parseBootSections(text: string): Map<string, string[]> {
  const sections = new Map<string, string[]>([[DEFAULT_SECTION, []]]);
  let current = DEFAULT_SECTION;
  for (const line of text.split('\n')) {
    const heading = line.trim().match(/^\[(.+)\]$/);
    if (heading?.[1]) {
      current = heading[1];
      if (!sections.has(current)) sections.set(current, []);
      continue;
    }
    sections.get(current)?.push(line);
  }
  return sections;
}

/** The key half of `arm_freq=2000`, or null for a comment, a blank line or anything else. */
export function keyOf(line: string): string | null {
  const text = line.trim();
  if (text.length === 0 || text.startsWith('#') || text.startsWith('[')) return null;
  const equals = text.indexOf('=');
  return equals > 0 ? text.slice(0, equals).trim() : null;
}

/**
 * Whether a line sets this key.
 *
 * Matched on the whole `key=` prefix rather than on a parsed key, because a setting's key can
 * itself contain an `=`: `dtparam=audio=on` is the `dtparam=audio` setting, and `arm_freq=` must
 * not match `arm_freq_min=`. Comments are excluded first, so a commented-out setting is never
 * mistaken for one in force.
 */
const setsKey = (line: string, key: string) => keyOf(line) !== null && line.trim().startsWith(`${key}=`);

/** Whether a section already carries this exact line, ignoring surrounding whitespace. */
const carries = (lines: string[], wanted: string) => lines.some((line) => line.trim() === wanted.trim());

/**
 * Put the settings and overlays into one section, leaving every other byte of the file alone.
 *
 * A scalar setting replaces the line with the same key *within that section only* — the same key in
 * another section is a different fact about a different board. An overlay is appended when the
 * section does not already carry that exact line, and never replaces anything, because two
 * `dtoverlay` lines are two pieces of hardware rather than a contradiction.
 */
export function applyToSection(
  text: string,
  section: string,
  settings: Record<string, string>,
  overlays: string[],
): string {
  const lines = text.split('\n');
  const header = lines.findIndex((line) => line.trim() === `[${section}]`);

  // where this section's lines end: the next header, or the end of the file
  let end = header < 0 ? lines.length : header + 1;
  if (header >= 0) {
    while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;
  }
  const body = header < 0
    ? (section === DEFAULT_SECTION ? lines.slice(0, firstHeader(lines)) : [])
    : lines.slice(header + 1, end);

  const updated = [...body];
  for (const [key, value] of Object.entries(settings)) {
    const wanted = `${key}=${value}`;
    const at = updated.findIndex((line) => setsKey(line, key));
    if (at >= 0) updated[at] = wanted;
    else updated.push(wanted);
  }
  for (const overlay of overlays) {
    if (!carries(updated, overlay)) updated.push(overlay);
  }

  if (header >= 0) return [...lines.slice(0, header + 1), ...updated, ...lines.slice(end)].join('\n');
  if (section === DEFAULT_SECTION) {
    const at = firstHeader(lines);
    return [...updated, ...lines.slice(at)].join('\n');
  }
  // a section the file has never had: append it, rather than putting settings somewhere they would
  // be read under a filter nobody asked for
  return `${text.replace(/\n+$/, '')}\n\n[${section}]\n${updated.join('\n')}\n`;
}

/**
 * The overlays this machine actually has, as `dtoverlay` names.
 *
 * The complete legal set, and it is a directory listing rather than a union in this source. That is
 * the better answer for the same reason `/sys/fs/cgroup/cgroup.controllers` beats `/proc/cmdline`:
 * ask what is there rather than what should be. A hand-maintained list of overlay names would be
 * wrong the moment a firmware package updates, and wrong silently.
 *
 * It matters more than typing the scalar settings does. An unknown *key* is inert — the firmware
 * ignores it — but `dtoverlay=dwc3`, one letter from `dwc2`, is a line that looks right and
 * disables a piece of hardware. On a machine whose storage hangs off `dtparam=pciex1`, that is the
 * array.
 */
export async function readOverlays(host: Target, path?: string): Promise<string[]> {
  const directory = `${(path ?? BOOT_CANDIDATES[0] ?? '').replace(/\/[^/]*$/, '')}/overlays`;
  const asked = await ask(host, escalate(host, `ls -1 ${shellQuote(directory)} 2>/dev/null || true`));
  if (asked.code !== 0) return [];
  return asked.out
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line.endsWith('.dtbo'))
    .map((line) => line.slice(0, -'.dtbo'.length))
    .sort();
}

/**
 * The overlay a line asks for, or null when the line is not a `dtoverlay`.
 *
 * `dtoverlay=dwc2,dr_mode=host` names `dwc2`; everything after the first comma is that overlay's
 * own parameters and is its business rather than ours. `dtparam=` lines name no overlay at all —
 * they configure the base device tree — so they are not checkable this way and are left alone.
 */
export function overlayNameOf(line: string): string | null {
  const text = line.trim();
  if (!text.startsWith('dtoverlay=')) return null;
  const value = text.slice('dtoverlay='.length);
  return (value.split(',')[0] ?? '').trim() || null;
}

/** Which of these lines name an overlay the machine does not have. */
export function unknownOverlays(lines: string[], available: string[]): string[] {
  const known = new Set(available);
  return lines
    .map(overlayNameOf)
    .filter((name): name is string => name !== null && !known.has(name));
}

/** Where the first `[section]` header is, or the end of the file. */
function firstHeader(lines: string[]): number {
  const at = lines.findIndex((line) => /^\s*\[.+\]\s*$/.test(line));
  return at < 0 ? lines.length : at;
}

/** Take exactly these lines out of one section, and nothing else with them. */
export function removeFromSection(
  text: string,
  section: string,
  settings: Record<string, string>,
  overlays: string[],
): string {
  const keys = new Set(Object.keys(settings));
  const wanted = new Set(overlays.map((overlay) => overlay.trim()));
  const lines = text.split('\n');
  const header = lines.findIndex((line) => line.trim() === `[${section}]`);
  const from = header < 0 ? 0 : header + 1;
  let end = header < 0 ? firstHeader(lines) : from;
  if (header >= 0) {
    while (end < lines.length && !/^\s*\[.+\]\s*$/.test(lines[end] ?? '')) end += 1;
  }
  const kept = lines
    .slice(from, end)
    .filter((line) => ![...keys].some((key) => setsKey(line, key)) && !wanted.has(line.trim()));
  return [...lines.slice(0, from), ...kept, ...lines.slice(end)].join('\n');
}

/** `arm_freq=2000` out of `vcgencmd get_config int`, which reports what the firmware resolved. */
export function parseVcgencmd(out: string): Record<string, string> {
  const resolved: Record<string, string> = {};
  for (const line of out.split('\n')) {
    const text = line.trim();
    const equals = text.indexOf('=');
    if (equals <= 0) continue;
    resolved[text.slice(0, equals)] = text.slice(equals + 1);
  }
  return resolved;
}

/** The file, the section, and what the firmware made of it — or null where there is no such file. */
export async function readBootConfig(
  host: Target,
  section: string,
  path?: string,
): Promise<{ path: string; lines: string[]; effective: Record<string, string> } | null> {
  const candidates = path ? [path] : BOOT_CANDIDATES;
  const tests = candidates.map((candidate) =>
    `test -f ${shellQuote(candidate)} && { echo ${shellQuote(candidate)}; cat ${shellQuote(candidate)}; exit 0; }`);
  const asked = await ask(host, escalate(host,
    `{ ${tests.join('; ')}; exit 9; }`,
  ));
  if (asked.code === 9) return null;
  if (asked.code !== 0) throw new Error(`could not read the boot configuration: ${asked.err.trim()}`);

  const split = asked.out.indexOf('\n');
  const found = asked.out.slice(0, split).trim();
  const text = asked.out.slice(split + 1);

  // what the firmware resolved, which is a different question from what the file says. Absent on a
  // machine that is not a Pi, and that is an empty answer rather than a failure
  const resolved = await ask(host, escalate(host, 'vcgencmd get_config int 2>/dev/null || true'));
  return {
    path: found,
    lines: parseBootSections(text).get(section) ?? [],
    effective: resolved.code === 0 ? parseVcgencmd(resolved.out) : {},
  };
}

function providerFor(host: Target): pulumi.dynamic.ResourceProvider<BootConfigArgs, BootConfigState> {
  const settle = async (args: BootConfigArgs): Promise<BootConfigState> => {
    const section = args.section ?? DEFAULT_SECTION;
    const settings = renderSettings(args.settings ?? {}, args.unchecked ?? {});
    const overlays = args.overlays ?? [];

    const before = await readBootConfig(host, section, args.path);
    if (!before) {
      const looked = args.path ?? BOOT_CANDIDATES.join(' or ');
      throw new Error(`no boot configuration at ${looked} on ${describe(host)}: this is not an image that boots that way`);
    }

    // checked against the machine rather than against a list in this source: an overlay name that
    // does not exist is a line that looks correct and silently disables hardware, and a firmware
    // package can add or remove one at any time
    const available = await readOverlays(host, before.path);
    const unknown = available.length > 0 ? unknownOverlays(overlays, available) : [];
    if (unknown.length > 0) {
      throw new Error(
        `${describe(host)} has no overlay named ${unknown.join(', ')}. ` +
        `The overlays it does have are in ${before.path.replace(/\/[^/]*$/, '')}/overlays; ` +
        `\`dtoverlay -h <name>\` documents one. A name that does not exist is not an error at boot — ` +
        `the line is simply ignored and the hardware it would have enabled is missing.`,
      );
    }

    const current = await must(host, escalate(host, `cat ${shellQuote(before.path)}`));
    const updated = applyToSection(current, section, settings, overlays);
    // an update that changes nothing does nothing, and on this file that matters more than usual:
    // it is on the partition the machine boots from
    if (updated !== current) await must(host, escalate(host, heredoc(before.path, updated)));

    const actual = await readBootConfig(host, section, before.path);
    if (!actual) throw new Error(`wrote ${before.path} but it is no longer there`);
    return {
      section, settings, overlays, path: actual.path,
      effective: actual.effective,
      overridden: disagreeing(settings, actual.effective),
    };
  };

  return {
    async create(args) {
      const state = await settle(args);
      return { id: `${state.path}#${state.section}`, outs: state };
    },

    async read(id, state) {
      const section = state?.section ?? id.split('#')[1] ?? DEFAULT_SECTION;
      const actual = await readBootConfig(host, section, state?.path);
      if (!actual) return { id: undefined, props: undefined };
      return {
        id,
        props: {
          settings: state?.settings ?? {},
          overlays: state?.overlays ?? [],
          ...state,
          ...state,
          section,
          path: actual.path,
          effective: actual.effective,
          overridden: disagreeing(state?.settings ?? {}, actual.effective),
        },
      };
    },

    async update(_id, _old, args) {
      return { outs: await settle(args) };
    },

    async diff(_id, old, args) {
      const section = args.section ?? DEFAULT_SECTION;
      const settings = renderSettings(args.settings ?? {}, args.unchecked ?? {});
      const overlays = args.overlays ?? [];
      return {
        changes: providerChanged(old, args)
          || old.section !== section
          || JSON.stringify(old.settings) !== JSON.stringify(settings)
          || old.overlays.join('\n') !== overlays.join('\n'),
        // a different section is a different fact about a different board, and the old one has to
        // be taken out rather than left applying to something nothing describes
        replaces: old.section !== section ? ['section'] : [],
        stables: [],
        deleteBeforeReplace: true,
      };
    },

    async delete(id, state) {
      // the state is what says which lines were ours: the id carries the file and the section, and
      // removing every line in a section would take the ones somebody else put there
      const [path = ''] = id.split('#');
      const current = await must(host, escalate(host, `cat ${shellQuote(path)}`));
      const without = removeFromSection(current, state.section, state.settings ?? {}, state.overlays ?? []);
      await must(host, escalate(host, heredoc(path, without)));
    },
  };
}

/** Lines in a Pi's boot configuration, in the section that decides whether they apply at all. */
export class BootConfig extends pulumi.dynamic.Resource {
  declare readonly section: pulumi.Output<string>;
  declare readonly path: pulumi.Output<string>;
  declare readonly effective: pulumi.Output<Record<string, string>>;

  constructor(name: string, host: Target, args: BootConfigArgs, opts?: pulumi.CustomResourceOptions) {
    super(providerFor(host), name, {
      section: DEFAULT_SECTION,
      settings: {},
      unchecked: {},
      overlays: [],
      path: undefined,
      effective: undefined,
      overridden: undefined,
      ...args,
    }, withLegacyAlias(opts), 'homelab', 'BootConfig');
  }
}
