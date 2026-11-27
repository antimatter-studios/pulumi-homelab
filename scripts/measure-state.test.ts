import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { measureState } from './measure-state.ts';

const raw = readFileSync(new URL('./fixtures/state-export.json', import.meta.url), 'utf8');

describe('measuring a state export', () => {
  const m = measureState(raw);

  it('counts the whole file', () => {
    expect(m.bytes).toBe(Buffer.byteLength(raw));
  });

  it('counts every __provider key and value, inputs and outputs', () => {
    // `"__provider":"AAAA"` is 19 bytes, `"__provider":"BBBBBB"` 21, each stored twice
    expect(m.provider_bytes).toBe(2 * 19 + 2 * 21);
    expect(m.provider_copies).toBe(4);
  });

  it('counts distinct __provider blobs', () => {
    expect(m.provider_distinct_blobs).toBe(2);
  });

  it('splits resources into dynamic, plugin and the rest', () => {
    expect(m).toMatchObject({ resources: 5, dynamic: 2, plugin: 1, other: 2, dynamic_types: 1 });
  });

  it('reads the raw backend checkpoint too', () => {
    const dep = JSON.parse(raw).deployment;
    const c = measureState(JSON.stringify({ checkpoint: { latest: dep } }));
    expect(c).toMatchObject({ provider_bytes: 80, dynamic: 2, plugin: 1 });
  });
});
