import { describe, expect, it } from 'vitest';
import { journaldFile, overriddenBy, parseEffective } from './journald.ts';

/**
 * The file this resource writes is not the last word on what journald does — a drop-in sorting
 * later wins. So the parsing here is of systemd's own merged view, and the thing being tested is
 * that the answer is the one that wins rather than the first one seen.
 */
const CAT_CONFIG = `# /etc/systemd/journald.conf
#  SPDX-License-Identifier: LGPL-2.1-or-later
[Journal]
#Storage=auto
#Compress=yes
#SystemMaxUse=

# /etc/systemd/journald.conf.d/logs.conf
[Journal]
Storage=volatile
SystemMaxUse=200M

# /etc/systemd/journald.conf.d/zz-override.conf
[Journal]
Storage=persistent
`;

describe('reading the configuration journald actually ends up with', () => {
  it('takes the last assignment, which is the one systemd takes', () => {
    // the first Storage= in the output is the distribution's commented default and the second is
    // ours; the answer is the third, in a file that sorts after ours
    expect(parseEffective(CAT_CONFIG).Storage).toBe('persistent');
  });

  it('keeps a setting nothing else overrides', () => {
    expect(parseEffective(CAT_CONFIG).SystemMaxUse).toBe('200M');
  });

  it('does not read a commented default as a setting', () => {
    // `#Compress=yes` is the shipped file documenting a default rather than choosing one, and
    // reading those as settings would report values nobody had chosen
    expect(parseEffective(CAT_CONFIG).Compress).toBeUndefined();
  });

  it('ignores section headers and the file-name banners', () => {
    expect(Object.keys(parseEffective(CAT_CONFIG)).sort()).toEqual(['Storage', 'SystemMaxUse']);
  });
});

describe('noticing that something else won', () => {
  it('names the setting another file overrides', () => {
    // not drift to fix by rewriting our own file — we would lose the same argument on the next run.
    // It is something a person has to see
    const effective = parseEffective(CAT_CONFIG);
    expect(overriddenBy({ Storage: 'volatile', SystemMaxUse: '200M' }, effective)).toEqual(['Storage']);
  });

  it('says nothing when the whole drop-in is in force', () => {
    expect(overriddenBy({ SystemMaxUse: '200M' }, parseEffective(CAT_CONFIG))).toEqual([]);
  });

  it('reports a setting journald never understood', () => {
    // a misspelled key is accepted by this resource and ignored by journald, and never appearing in
    // the effective configuration is exactly how that shows up
    expect(overriddenBy({ Storrage: 'volatile' }, parseEffective(CAT_CONFIG))).toEqual(['Storrage']);
  });
});

describe('writing the drop-in', () => {
  it('writes a [Journal] section and says who wrote it', () => {
    const file = journaldFile({ Storage: 'volatile', SystemMaxUse: '200M' });
    expect(file).toContain('[Journal]\nStorage=volatile\nSystemMaxUse=200M\n');
    expect(file.startsWith('# Managed by Pulumi.')).toBe(true);
  });

  it('round-trips through the parser it will be read back with', () => {
    // the write and the read have to agree about the representation, or the resource reports drift
    // on a file it wrote itself
    const settings = { Storage: 'volatile', MaxRetentionSec: '7day' };
    expect(parseEffective(journaldFile(settings))).toEqual(settings);
  });
});
