import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';

// Backend git.FileDiff response shape (JSON tags are snake_case — see
// server/internal/git/diff.go). `raw_patch` holds the full unified diff for the
// single file, including its `diff --git` header, so it can be fed directly to
// parseUnifiedDiff().
export interface GitFileDiff {
  path: string;
  status: string; // added|modified|deleted|renamed
  additions: number;
  deletions: number;
  hunks: GitDiffHunk[];
  raw_patch: string;
}

export interface GitDiffHunk {
  old_start: number;
  old_count: number;
  new_start: number;
  new_count: number;
  content: string;
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
