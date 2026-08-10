import { describe, it, expect } from 'vitest';
import { parseUnifiedDiff } from './use-diff';

describe('parseUnifiedDiff', () => {
  it('parses a normal content change into hunks', () => {
    const patch = [
      'diff --git a/s.sh b/s.sh',
      'index 69d7334..4614cec 100644',
      '--- a/s.sh',
      '+++ b/s.sh',
      '@@ -1,3 +1,4 @@',
      ' #!/usr/bin/env bash',
      ' echo hi',
      '+echo bye',
      '',
    ].join('\n');
    const [file] = parseUnifiedDiff(patch);
    expect(file.status).toBe('modified');
    expect(file.isBinary).toBeFalsy();
    expect(file.hunks).toHaveLength(1);
    expect(file.additions).toBe(1);
  });

  // Regression: a shell script whose only change is the executable bit produces
  // a zero-hunk diff. It must be recognized as a mode change, NOT as binary.
  it('marks a mode-only change (chmod +x) as text with mode info, not binary', () => {
    const patch = [
      'diff --git a/deploy/self/smoke/issue-bulk-ops.sh b/deploy/self/smoke/issue-bulk-ops.sh',
      'old mode 100644',
      'new mode 100755',
      '',
    ].join('\n');
    const [file] = parseUnifiedDiff(patch);
    expect(file.hunks).toHaveLength(0);
    expect(file.isBinary).toBeFalsy();
    expect(file.oldMode).toBe('100644');
    expect(file.newMode).toBe('100755');
  });

  it('marks a pure rename (no content change) as renamed text, not binary', () => {
    const patch = [
      'diff --git a/old.sh b/new.sh',
      'similarity index 100%',
      'rename from old.sh',
      'rename to new.sh',
      '',
    ].join('\n');
    const [file] = parseUnifiedDiff(patch);
    expect(file.hunks).toHaveLength(0);
    expect(file.isBinary).toBeFalsy();
    expect(file.status).toBe('renamed');
    expect(file.oldPath).toBe('old.sh');
    expect(file.path).toBe('new.sh');
  });

  it('flags a genuinely binary file via the "Binary files" marker', () => {
    const patch = [
      'diff --git a/logo.png b/logo.png',
      'index 0000000..4d8e0bf 100644',
      'Binary files a/logo.png and b/logo.png differ',
      '',
    ].join('\n');
    const [file] = parseUnifiedDiff(patch);
    expect(file.hunks).toHaveLength(0);
    expect(file.isBinary).toBe(true);
  });
});
