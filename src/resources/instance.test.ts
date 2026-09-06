import { describe, expect, it } from 'vitest';
import { escapeInstance, instanceUnit } from './instance.ts';

/**
 * An instance name goes into a filename and into systemd's own name resolution, so getting the
 * escaping wrong produces a unit that either does not exist or is a different one.
 */
describe('escaping an instance name', () => {
  it('leaves a hostname exactly as it is, which is the common case', () => {
    // dots and alphanumerics are legal, so the ordinary case costs nothing and stays readable
    expect(escapeInstance('photos.example.local')).toBe('photos.example.local');
  });

  it('turns a path separator into a dash, as systemd does', () => {
    // which is why a path-like instance cannot simply be passed through: `mnt/data` and
    // `mnt-data` are the same unit to systemd and different strings to us
    expect(escapeInstance('mnt/data')).toBe('mnt-data');
  });

  it('escapes a leading dot, which would otherwise be a hidden file', () => {
    expect(escapeInstance('.hidden')).toBe('\\x2ehidden');
  });

  it('escapes anything else as its bytes', () => {
    expect(escapeInstance('a b')).toBe('a\\x20b');
    expect(escapeInstance('a@b')).toBe('a\\x40b');
  });

  it('escapes a multi-byte character as every one of its bytes', () => {
    // one character, two bytes, two escapes — a per-character escape would produce a name systemd
    // does not resolve to anything
    expect(escapeInstance('é')).toBe('\\xc3\\xa9');
  });

  it('keeps the characters systemd allows through', () => {
    expect(escapeInstance('a:b_c.d')).toBe('a:b_c.d');
  });

  it('leaves an empty instance empty rather than inventing something', () => {
    expect(escapeInstance('')).toBe('');
  });
});

describe('naming the unit', () => {
  it('builds the name systemd knows it by', () => {
    expect(instanceUnit('avahi-alias', 'photos.example.local'))
      .toBe('avahi-alias@photos.example.local.service');
  });

  it('escapes the instance on the way in', () => {
    expect(instanceUnit('backup', 'mnt/data')).toBe('backup@mnt-data.service');
  });

  it('takes a suffix for a template that is not a service', () => {
    expect(instanceUnit('systemd-backlight', 'leds:x', 'mount')).toBe('systemd-backlight@leds:x.mount');
  });
});
