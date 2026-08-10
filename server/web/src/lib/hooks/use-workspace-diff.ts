import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Workspace } from '@/types/api';
import type { GitFileDiff } from './use-file-diff';

/**
 * One changed file in the "vs baseline" diff. Mirrors the backend
 * `git.FileDiff` JSON shape (GET /workspaces/:id/diff). The grouped file list
 * only reads the summary fields; the line-level viewer loads hunks on demand.
 */
export interface WorkspaceFileDiff {
  path: string;
  status: string; // added | modified | deleted | renamed | copied
  additions: number;
  deletions: number;
}

/**
 * One repository's segment of GET /workspaces/:id/diff. The backend resolves the
 * repository_id + worktree_path for each worktree itself (reverse-resolving from
 * the worktree directory when the stored association is stale), so the client
 * never has to re-resolve a repo by name. repository_id is 0 when no registered
 * repository matched the worktree's source — the changes still list, but the
 * repo-scoped actions (jump / line-level diff) are disabled for that group.
 */
export interface RepoDiffResponse {
  name: string;
  repository_id: number;
  worktree_path: string;
  base_branch: string;
  /** The worktree's own checked-out branch (HEAD). */
  current_branch: string;
  // Per-file diffs. The list view reads only the summary fields. The backend
  // includes hunks + raw_patch ONLY for unresolved groups (repository_id 0),
  // whose line-level viewer reuses them directly; resolved groups are shipped
  // summary-only and re-fetch the line-level diff by id on demand.
  files: GitFileDiff[];
}

interface CommentResponse {
  id: number;
  repo: string;
  file_path: string;
  line_number?: number;
}

/** A single file row, enriched with its review-comment count. */
export interface DiffFileRow extends WorkspaceFileDiff {
  commentCount: number;
}

/** One repository's segment in the grouped file list. */
export interface RepoDiffGroup {
  /** Repository display name (the diff map key). */
  name: string;
  /** Repository id, when resolvable — for the on-demand line-level diff load. */
  repoId: string | null;
  /** Baseline branch this repo is diffed against (base...HEAD). */
  baseBranch: string;
  /** The worktree's own checked-out branch (HEAD), shown alongside the base. */
  currentBranch: string;
  /** Worktree absolute path, when resolvable (for VS Code / sync actions). */
  worktreePath: string | null;
  /** Commits ahead of the baseline already folded into this diff. */
  aheadCount: number;
  files: DiffFileRow[];
  /**
   * Full per-file diffs from the workspace diff response (hunks + raw_patch).
   * The line-level viewer renders these directly for repoId-null groups, which
   * have no repository to lazily re-fetch the diff by id.
   */
  rawFiles: GitFileDiff[];
  additions: number;
  deletions: number;
}

export interface WorkspaceDiffData {
  repos: RepoDiffGroup[];
  totalFiles: number;
  totalAdditions: number;
  totalDeletions: number;
  /** Total commits ahead of baseline across all repos (weak commit hint). */
  totalAhead: number;
  /** Distinct baseline branch names across repos. */
  baseBranches: string[];
  isLoading: boolean;
}

/**
 * Aggregates the workspace "vs baseline" diff into a per-repository grouping
 * ready for the changes panel: each repo carries its baseline branch, +/−
 * totals, ahead-of-base commit count, and per-file comment counts.
 *
 * The backend (GET /workspaces/:id/diff) already resolves each worktree's
 * repository_id + worktree_path — reverse-resolving from the worktree directory
 * when the stored association is stale — so there is no name-based re-lookup
 * here. A group whose source repo matched no registered repository arrives with
 * repository_id 0; its changes still list, but repoId is left null so the
 * repo-scoped actions (jump / line-level diff) are disabled for that group only.
 * Ahead-of-base commit count is the one field still joined from the cached
 * sidebar worktrees, by name.
 */
export function useWorkspaceDiff(workspaceId: string): WorkspaceDiffData {
  const { data: diffGroups, isLoading: diffLoading } = useQuery({
    queryKey: ['workspace', workspaceId, 'diff'],
    queryFn: () => api.get<RepoDiffResponse[]>(`/workspaces/${workspaceId}/diff`),
    retry: 1,
  });

  // Shares the cache with WorkspacePage's workspace query (same key).
  const { data: workspace } = useQuery({
    queryKey: ['workspace', workspaceId],
    queryFn: () => api.get<Workspace>(`/workspaces/${workspaceId}`),
    staleTime: 30_000,
  });

  const { data: comments } = useQuery({
    queryKey: ['workspace', workspaceId, 'comments'],
    queryFn: () => api.get<CommentResponse[]>(`/workspaces/${workspaceId}/comments`),
    staleTime: 10_000,
  });

  return useMemo(() => {
    const groups = diffGroups ?? [];

    // repo name -> ahead-of-base commit count (the only field still sourced from
    // the cached sidebar worktrees; base branch now comes from the diff itself).
    const aheadByRepo = new Map<string, number>();
    for (const wt of workspace?.worktrees ?? []) {
      aheadByRepo.set(wt.repo_name, wt.ahead_count);
    }
    // repo + file path -> comment count (repo-scoped: same path can exist in
    // multiple repos of one workspace).
    const commentKey = (repo: string, path: string) => `${repo} ${path}`;
    const commentByPath = new Map<string, number>();
    for (const c of comments ?? []) {
      const k = commentKey(c.repo ?? '', c.file_path);
      commentByPath.set(k, (commentByPath.get(k) ?? 0) + 1);
    }

    // One group per worktree the backend returned. repository_id 0 (no source
    // repo matched) maps to repoId null, which disables the jump-to-repo action
    // and the line-level repo-diff fetch (both gated on repoId) — the changes
    // still list. Every attached repo shows, even at 0 changes (collapsible).
    const repos: RepoDiffGroup[] = groups
      .map((g) => {
        const files: DiffFileRow[] = (g.files ?? []).map((f) => ({
          ...f,
          commentCount: commentByPath.get(commentKey(g.name, f.path)) ?? 0,
        }));
        return {
          name: g.name,
          repoId: g.repository_id > 0 ? String(g.repository_id) : null,
          baseBranch: g.base_branch ?? '',
          currentBranch: g.current_branch ?? '',
          aheadCount: aheadByRepo.get(g.name) ?? 0,
          worktreePath: g.worktree_path || null,
          files,
          // Full line-level diffs are only retained for unresolved groups
          // (no repository to lazily re-fetch by id); resolved groups get them
          // from useRepoDiff, and the backend ships them summary-only anyway.
          rawFiles: g.repository_id > 0 ? [] : (g.files ?? []),
          additions: files.reduce((s, f) => s + f.additions, 0),
          deletions: files.reduce((s, f) => s + f.deletions, 0),
        };
      })
      .sort((a, b) => a.name.localeCompare(b.name));

    return {
      repos,
      totalFiles: repos.reduce((s, r) => s + r.files.length, 0),
      totalAdditions: repos.reduce((s, r) => s + r.additions, 0),
      totalDeletions: repos.reduce((s, r) => s + r.deletions, 0),
      totalAhead: repos.reduce((s, r) => s + r.aheadCount, 0),
      baseBranches: Array.from(new Set(repos.map((r) => r.baseBranch).filter(Boolean))),
      isLoading: diffLoading,
    };
  }, [diffGroups, workspace, comments, diffLoading]);
}
