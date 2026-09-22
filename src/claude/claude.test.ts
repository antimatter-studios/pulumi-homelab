import { describe, expect, it } from 'vitest';
import { profileRefusal, sharedTarget, SHARED_DEFAULTS } from './index.ts';

describe('where a shared entry lives', () => {
  it('joins the backend and the entry', () => {
    expect(sharedTarget('/home/agent/.claude', 'projects')).toBe('/home/agent/.claude/projects');
  });

  it('does not double the separator when the backend has a trailing slash', () => {
    // a doubled slash resolves the same and reads back differently, which is drift for ever
    expect(sharedTarget('/home/agent/.claude/', 'sessions')).toBe('/home/agent/.claude/sessions');
  });

  it('tolerates several trailing slashes', () => {
    expect(sharedTarget('/home/agent/.claude///', 'plugins')).toBe('/home/agent/.claude/plugins');
  });

  it('keeps an absolute target, because the program resolving it is not in this directory', () => {
    expect(sharedTarget('/mnt/storage/t3code/.claude', 'settings.json'))
      .toBe('/mnt/storage/t3code/.claude/settings.json');
  });
});

/**
 * The loop this refuses is created successfully and fails on first use, with a message about a
 * corrupt installation rather than about a description that asked for something impossible.
 */
describe('refusing a profile that is its own backend', () => {
  const base = { account: 'agent', shared: '/home/agent/.claude', dir: '/home/agent/.claude-work' };

  it('accepts a profile beside the backend', () => {
    expect(profileRefusal(base)).toBeUndefined();
  });

  it('refuses a profile that is the backend', () => {
    expect(profileRefusal({ ...base, dir: '/home/agent/.claude' }))
      .toMatch(/dir and shared are the same directory/);
  });

  it('refuses it when only a trailing slash makes them look different', () => {
    expect(profileRefusal({ ...base, dir: '/home/agent/.claude/' }))
      .toMatch(/dir and shared are the same directory/);
  });

  it('accepts a profile nested under the backend, which is odd but not a loop', () => {
    expect(profileRefusal({ ...base, dir: '/home/agent/.claude/other' })).toBeUndefined();
  });
});

describe('what is shared by default', () => {
  it('shares work, and nothing that identifies an account', () => {
    expect(SHARED_DEFAULTS.map((entry) => entry.name))
      .toEqual(['projects', 'sessions', 'session-env', 'plugins', 'settings.json']);
  });

  /**
   * Credentials are the whole reason a profile exists. Sharing any of these would make two profiles
   * one account with extra steps.
   */
  it('shares no credential', () => {
    const names = SHARED_DEFAULTS.map((entry) => entry.name);
    for (const secret of ['.credentials.json', '.claude.json', 'history.jsonl']) {
      expect(names).not.toContain(secret);
    }
  });

  it('gives conversation content a private mode and the rest the group one', () => {
    const mode = (name: string) => SHARED_DEFAULTS.find((entry) => entry.name === name)?.mode;
    expect(mode('projects')).toBe('0700');
    expect(mode('sessions')).toBe('0700');
    expect(mode('session-env')).toBe('0775');
    expect(mode('plugins')).toBe('0775');
  });

  it('declares settings.json as a file, so nothing writes an empty one', () => {
    expect(SHARED_DEFAULTS.find((entry) => entry.name === 'settings.json')?.kind).toBe('file');
    expect(SHARED_DEFAULTS.find((entry) => entry.name === 'settings.json')?.mode).toBeUndefined();
  });
});
