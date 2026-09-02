import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// Backend git.FileDiff response shape (JSON tags are snake_case — see
// server/internal/git/diff.go).
//
// The backend is the ONLY diff parser: it decomposes git's unified output into
// hunks and lines with resolved line numbers, so the client renders straight
// from this structure. There is deliberately no `raw_patch` here — diff
// endpoints no longer ship it to rendering clients, because a text patch on the
// wire is what invited a second, divergent parser in the first place (git's
// "Binary files … differ" marker used to be visible only to the frontend one).
export interface GitFileDiff {
  path: string;
  /** Pre-change path; present only for renames and copies. */
  old_path?: string;
  status: string; // added|modified|deleted|renamed|copied
  additions: number;
  deletions: number;
  /**
   * True only when git reported the file as an actual binary blob
   * ("Binary files ... differ" / "GIT binary patch"). A diff with zero hunks is
   * NOT necessarily binary — mode-only changes (e.g. `chmod +x` on a shell
   * script), pure renames and empty files also produce no hunks. Consumers must
   * gate the "can't preview text" message on this flag, never on
   * `hunks.length === 0`.
   */
  is_binary?: boolean;
  /** File mode before/after, when the diff carries an `old mode`/`new mode` pair. */
  old_mode?: string;
  new_mode?: string;
  hunks: GitDiffHunk[];
}

export interface GitDiffHunk {
  old_start: number;
  old_count: number;
  new_start: number;
  new_count: number;
  /** Section heading git appends after the closing `@@` (usually the enclosing function). */
  header?: string;
  lines: GitDiffLine[];
}

export interface GitDiffLine {
  type: 'context' | 'add' | 'delete';
  content: string;
  /**
   * 1-based line numbers, omitted (undefined) when the line does not exist on
   * that side: an added line has no `old_line`, a deleted line no `new_line`.
   * Resolved server-side, so rendering needs no running counters.
   */
  old_line?: number;
  new_line?: number;
  /** The line is followed by git's "\ No newline at end of file". */
  no_newline?: boolean;
}

/**
 * Lazily fetch the full "vs baseline" (base...HEAD + uncommitted + untracked)
 * diff for one repository in a workspace. The line-level diff viewer selects the
 * file it needs from this array, so opening several files in the same repo
 * shares a single cached request. Pass enabled=false to defer the fetch until a
 * file is actually opened.
 */
export function useRepoDiff(
  workspaceId: string | null,
  repoId: string | number | null,
  enabled = true,
) {
  return useQuery({
    queryKey: ['workspace', workspaceId, 'repositories', String(repoId), 'diff'],
    queryFn: () =>
      api.get<GitFileDiff[]>(`/workspaces/${workspaceId}/repositories/${repoId}/diff`),
    enabled: enabled && !!workspaceId && repoId != null,
    retry: 1,
  });
}
