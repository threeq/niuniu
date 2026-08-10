import { describe, expect, it, vi } from 'vitest';

vi.mock('@/stores/auth-store', () => ({ getAccessToken: () => 'tok123' }));

import { getRepoFileContentUrl } from './repo-file-url';

describe('getRepoFileContentUrl', () => {
  it('builds a raw-mode repo file URL carrying the token and an encoded path', () => {
    const url = getRepoFileContentUrl('42', 'src/a b.ts');
    expect(url.startsWith('/api/repositories/42/files/content?')).toBe(true);
    const qs = new URLSearchParams(url.split('?')[1]);
    expect(qs.get('path')).toBe('src/a b.ts');
    expect(qs.get('mode')).toBe('raw');
    expect(qs.get('token')).toBe('tok123');
  });
});
