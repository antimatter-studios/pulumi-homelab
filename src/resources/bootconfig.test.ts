import { describe, expect, it } from 'vitest';
import {
  applyToSection, keyOf, parseBootSections, parseVcgencmd, removeFromSection, renderSettings, overlayNameOf, unknownOverlays,
} from './bootconfig.ts';

/**
 * `config.txt` has conditional filter sections, and a setting under a section that does not match
 * the board **is not an error** — the firmware reads it, decides it does not apply, and carries on.
 * So a correct-looking line can do nothing at all, and the symptom is a missing drive rather than
 * anything mentioning boot configuration. That is why the section is part of the identity.
 */
const REAL = `# For more options and information see http://rptl.io/configtxt
dtparam=audio=on
camera_auto_detect=1

[all]
# PCIe / NVMe.
# NOTE: on the previous card these lived under [cm5], which only matches a
# Compute Module 5 - never a Pi 5 Model B - so they silently never applied.
dtparam=pciex1
#dtparam=pciex1_gen=3
dtoverlay=vc4-kms-v3d

[pi5]
arm_freq=2400

[cm5]
dtoverlay=dwc2,dr_mode=host
`;

describe('reading the filter sections', () => {
  it('puts lines before any header in the implicit all section', () => {
    // a file that has never been sectioned is entirely [all], and treating those lines as belonging
    // to nothing would hide settings that are plainly in force
    const all = parseBootSections(REAL).get('all') ?? [];
    expect(all).toContain('dtparam=audio=on');
    expect(all).toContain('camera_auto_detect=1');
  });

  it('keeps each section separate, because the same line means different things in each', () => {
    expect(parseBootSections(REAL).get('pi5')).toContain('arm_freq=2400');
    expect(parseBootSections(REAL).get('cm5')).toContain('dtoverlay=dwc2,dr_mode=host');
    expect(parseBootSections(REAL).get('pi5')).not.toContain('dtparam=pciex1');
  });

  it('has nothing to say about a section the file does not have', () => {
    expect(parseBootSections(REAL).get('cm4')).toBeUndefined();
  });
});

describe('telling a setting from a comment', () => {
  it('reads the key of a setting', () => {
    expect(keyOf('arm_freq=2400')).toBe('arm_freq');
    expect(keyOf('  dtparam=pciex1  ')).toBe('dtparam');
  });

  it('does not read a commented-out setting as one', () => {
    // `#dtparam=pciex1_gen=3` is a decision deliberately not taken, and replacing it would be
    // taking it on somebody's behalf
    expect(keyOf('#dtparam=pciex1_gen=3')).toBeNull();
    expect(keyOf('# NOTE: on the previous card these lived under [cm5]')).toBeNull();
    expect(keyOf('[pi5]')).toBeNull();
    expect(keyOf('')).toBeNull();
  });
});

describe('putting lines into one section', () => {
  it('adds an overlay the section does not have', () => {
    const updated = applyToSection(REAL, 'all', {}, ['dtparam=nvme']);
    expect(parseBootSections(updated).get('all')).toContain('dtparam=nvme');
  });

  it('does not touch a section it was not asked about', () => {
    // the same key in another section is a different fact about a different board
    const updated = applyToSection(REAL, 'pi5', { arm_freq: '2000' }, []);
    expect(parseBootSections(updated).get('pi5')).toContain('arm_freq=2000');
    expect(parseBootSections(updated).get('cm5')).toContain('dtoverlay=dwc2,dr_mode=host');
    expect(parseBootSections(updated).get('all')).toContain('dtparam=pciex1');
  });

  it('never collapses repeated overlay lines, which are separate hardware', () => {
    // a key-based upsert would turn two dtoverlay lines into one and silently remove a device
    const updated = applyToSection(REAL, 'all', {}, ['dtoverlay=dwc2,dr_mode=host']);
    const all = parseBootSections(updated).get('all') ?? [];
    expect(all).toContain('dtoverlay=vc4-kms-v3d');
    expect(all).toContain('dtoverlay=dwc2,dr_mode=host');
  });

  it('replaces a scalar setting in place rather than adding a second one', () => {
    const updated = applyToSection(REAL, 'pi5', { arm_freq: '2000' }, []);
    const pi5 = parseBootSections(updated).get('pi5') ?? [];
    expect(pi5.filter((line) => keyOf(line) === 'arm_freq')).toHaveLength(1);
  });

  it('keeps the comments, which are the only record of why a line exists', () => {
    const updated = applyToSection(REAL, 'all', {}, ['dtparam=nvme']);
    expect(updated).toContain('# NOTE: on the previous card these lived under [cm5]');
    expect(updated).toContain('#dtparam=pciex1_gen=3');
  });

  it('gives the same file whether it runs once or twice', () => {
    const once = applyToSection(REAL, 'all', {}, ['dtparam=pciex1']);
    expect(applyToSection(once, 'all', {}, ['dtparam=pciex1'])).toBe(once);
  });

  it('does nothing at all when the line is already there', () => {
    expect(applyToSection(REAL, 'all', {}, ['dtparam=pciex1'])).toBe(REAL);
  });

  it('creates a section the file has never had rather than guessing where to put it', () => {
    const updated = applyToSection(REAL, 'cm4', {}, ['dtoverlay=x']);
    expect(parseBootSections(updated).get('cm4')).toContain('dtoverlay=x');
    expect(parseBootSections(updated).get('all')).not.toContain('dtoverlay=x');
  });
});

describe('taking lines out again', () => {
  it('removes only what was named, from only the section named', () => {
    const without = removeFromSection(REAL, 'all', {}, ['dtparam=pciex1']);
    expect(parseBootSections(without).get('all')).not.toContain('dtparam=pciex1');
    expect(parseBootSections(without).get('all')).toContain('dtoverlay=vc4-kms-v3d');
    expect(parseBootSections(without).get('cm5')).toContain('dtoverlay=dwc2,dr_mode=host');
  });

  it('leaves the comments behind, because a note outlives the line it explains', () => {
    const without = removeFromSection(REAL, 'all', {}, ['dtparam=pciex1']);
    expect(without).toContain('# NOTE: on the previous card these lived under [cm5]');
  });
});

describe('asking the firmware what it actually parsed', () => {
  it('reads what vcgencmd resolved, which is the effective answer', () => {
    // the file says what was asked for; this says what the board made of it, and the difference is
    // what would have caught a setting sitting under a section that never matched
    expect(parseVcgencmd('arm_freq=2400\ntotal_mem=8192\n')).toEqual({
      arm_freq: '2400', total_mem: '8192',
    });
  });

  it('says nothing on a machine that has no vcgencmd', () => {
    expect(parseVcgencmd('')).toEqual({});
  });
});

/**
 * The asymmetry that argues for typed keys: an unknown key is mostly harmless, because the firmware
 * ignores what it does not recognise, so a typo does nothing. A **valid key with a bad value** is
 * what does not boot, and cannot be fixed over ssh.
 */
describe('rendering the typed settings', () => {
  it('writes booleans as the 1 and 0 the firmware reads', () => {
    // `arm_boost=true` is an unrecognised value on a recognised key, which is the quiet half of the
    // failure the type exists to prevent
    expect(renderSettings({ arm_boost: true, disable_overscan: false }))
      .toEqual({ arm_boost: '1', disable_overscan: '0' });
  });

  it('writes numbers as themselves', () => {
    // the open-ended ones stay numbers: a union would be wrong somewhere and right nowhere in
    // particular, since hdmi_mode means different things under different hdmi_group values
    expect(renderSettings({ gpu_mem: 128, hdmi_mode: 82 })).toEqual({ gpu_mem: '128', hdmi_mode: '82' });
  });

  it('spells dtparam=audio the way the file does, not the way the type does', () => {
    expect(renderSettings({ audio: 'on' })).toEqual({ 'dtparam=audio': 'on' });
  });

  it('replaces dtparam=audio rather than adding a second one', () => {
    // through `overlays` this would append, because repetition is what overlays are for — and the
    // machine would then carry both dtparam=audio=on and dtparam=audio=off
    const updated = applyToSection('[all]\ndtparam=audio=on\n', 'all', renderSettings({ audio: 'off' }), []);
    const all = parseBootSections(updated).get('all') ?? [];
    expect(all).toContain('dtparam=audio=off');
    expect(all).not.toContain('dtparam=audio=on');
  });

  it('does not let one key match another that merely starts the same way', () => {
    const updated = applyToSection('[all]\narm_freq_min=600\n', 'all', { arm_freq: '2400' }, []);
    const all = parseBootSections(updated).get('all') ?? [];
    expect(all).toContain('arm_freq_min=600');
    expect(all).toContain('arm_freq=2400');
  });

  it('leaves out what was never set, rather than writing a default nobody chose', () => {
    expect(renderSettings({ arm_boost: true })).toEqual({ arm_boost: '1' });
    expect(renderSettings({})).toEqual({});
  });

  it('carries the unchecked settings through unaltered', () => {
    expect(renderSettings({}, { pciex1_gen: '3' })).toEqual({ pciex1_gen: '3' });
  });

  it('lets an unchecked value override a typed one, visibly', () => {
    // the override is possible and it is written down at the call site, which is the point of the
    // escape hatch being named `unchecked` rather than `extra`
    expect(renderSettings({ gpu_mem: 128 }, { gpu_mem: '256' })).toEqual({ gpu_mem: '256' });
  });

  it('is the only way to reach the settings that stop a machine booting', () => {
    // arm_freq, over_voltage, kernel and initramfs are absent from BootSettings on purpose: not
    // because a type could validate them, but because the recovery is a card reader
    expect(renderSettings({}, { arm_freq: '3000', over_voltage: '6' }))
      .toEqual({ arm_freq: '3000', over_voltage: '6' });
  });

  it('renders into the same shape the file comparison uses', () => {
    const rendered = renderSettings({ gpu_mem: 76 });
    const updated = applyToSection('[pi5]\ngpu_mem=128\n', 'pi5', rendered, []);
    expect(parseBootSections(updated).get('pi5')).toContain('gpu_mem=76');
  });
});

/**
 * Overlay names are checked against the machine rather than against a list in this source — the
 * same argument as reading `/sys/fs/cgroup/cgroup.controllers` instead of `/proc/cmdline`. A
 * hand-maintained union would be wrong the moment a firmware package updated, and wrong silently.
 */
describe('checking overlay names against what the machine has', () => {
  const AVAILABLE = ['vc4-kms-v3d', 'dwc2', 'disable-bt', 'pi3-disable-wifi'];

  it('reads the overlay a line asks for', () => {
    expect(overlayNameOf('dtoverlay=vc4-kms-v3d')).toBe('vc4-kms-v3d');
  });

  it('stops at the first comma, since the rest is that overlay’s own parameters', () => {
    expect(overlayNameOf('dtoverlay=dwc2,dr_mode=host')).toBe('dwc2');
  });

  it('says nothing about a dtparam, which names no overlay', () => {
    // dtparam configures the base device tree rather than loading a file, so there is nothing to
    // check it against and pretending otherwise would reject a correct line
    expect(overlayNameOf('dtparam=pciex1')).toBeNull();
    expect(overlayNameOf('arm_boost=1')).toBeNull();
    expect(overlayNameOf('# dtoverlay=vc4-kms-v3d')).toBeNull();
  });

  it('catches the typo that would silently disable hardware', () => {
    // dwc3 is one letter from dwc2 and is not an error at boot: the line is ignored and the device
    // it would have enabled is simply missing
    expect(unknownOverlays(['dtoverlay=dwc3'], AVAILABLE)).toEqual(['dwc3']);
  });

  it('passes every name the machine actually has', () => {
    expect(unknownOverlays(['dtoverlay=vc4-kms-v3d', 'dtoverlay=dwc2,dr_mode=host'], AVAILABLE)).toEqual([]);
  });

  it('has no opinion about lines that are not overlays', () => {
    expect(unknownOverlays(['dtparam=pciex1', 'dtparam=audio=on'], AVAILABLE)).toEqual([]);
  });
});
