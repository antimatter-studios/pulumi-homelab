import { describe, expect, it } from 'vitest';
import { asAccount } from './installer.ts';

describe('running a vendor installer as the account', () => {
  it('starts from the account\'s own home, not the caller\'s directory', () => {
    const command = asAccount('t3code', '/mnt/storage/t3code', 'curl -fsSL https://example.com/install.sh | bash');
    expect(command).toContain("runuser -u 't3code'");
    expect(command).toContain('cd "$HOME" && curl');
    expect(command).toContain("HOME='/mnt/storage/t3code'");
  });
});
