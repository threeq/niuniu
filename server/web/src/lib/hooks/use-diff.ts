import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '@/lib/api';

// Diff file status types
export type DiffFileStatus = 'modified' | 'added' | 'deleted' | 'untracked' | 'renamed' | 'copied' | 'unchanged';

export interface DiffFile {
  path: string;
  oldPath?: string;
  status: DiffFileStatus;
  additions: number;
  deletions: number;
  hunks: DiffHunk[];
  /**
   * True only when git reported the file as an actual binary blob
   * ("Binary files ... differ"). A diff with zero hunks is NOT necessarily
   * binary — mode-only changes (e.g. `chmod +x` on a shell script) and pure
   * renames also produce no hunks. Consumers must gate the "can't preview text"
   * message on this flag, never on `hunks.length === 0`.
   */
  isBinary?: boolean;
  /** File mode before/after, when the diff carries an `old mode`/`new mode` pair. */
  oldMode?: string;
  newMode?: string;
}

export interface DiffHunk {
  oldStart: number;
  oldLines: number;
  newStart: number;
  newLines: number;
  lines: DiffLine[];
}

export interface DiffLine {
  type: 'context' | 'add' | 'delete' | 'header';
  content: string;
  oldLineNumber?: number;
  newLineNumber?: number;
}

export interface WorkspaceDiff {
  workspaceId: string;
  files: DiffFile[];
  totalAdditions: number;
  totalDeletions: number;
}

interface DiffResponse {
  files: DiffFile[];
  totalAdditions: number;
  totalDeletions: number;
}

interface CreateCommentData {
  filePath: string;
  line: number;
  body: string;
}

interface Comment {
  id: string;
  filePath: string;
  line: number;
  body: string;
  createdAt: string;
}

export function useDiff(workspaceId: string | null) {
  const queryClient = useQueryClient();

  const diffQuery = useQuery({
    queryKey: ['workspace', workspaceId, 'diff'],
    queryFn: () => api.get<DiffResponse>(`/workspaces/${workspaceId}/diff`),
    enabled: !!workspaceId,
    retry: 1,
  });

  const createCommentMutation = useMutation({
    mutationFn: (data: CreateCommentData) =>
      api.post<Comment>(`/workspaces/${workspaceId}/comments`, data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['workspace', workspaceId, 'diff'] });
    },
  });

  const commentsQuery = useQuery({
    queryKey: ['workspace', workspaceId, 'comments'],
    queryFn: () => api.get<Comment[]>(`/workspaces/${workspaceId}/comments`),
    enabled: !!workspaceId,
  });

  return {
    diff: diffQuery.data,
    files: diffQuery.data?.files || [],
    isLoading: diffQuery.isLoading,
    error: diffQuery.error,
    refetch: diffQuery.refetch,
    createComment: createCommentMutation.mutate,
    isCreatingComment: createCommentMutation.isPending,
    comments: commentsQuery.data || [],
  };
}

// Helper to parse unified diff format into structured DiffFile array
export function parseUnifiedDiff(diffText: string): DiffFile[] {
  const files: DiffFile[] = [];
  const hunkRegex = /^@@ -(\d+),?(\d*) \+(\d+),?(\d*) @@/;
  const lineRegex = /^([ +-])(.*)$/;

  let currentFile: DiffFile | null = null;
  let currentHunk: DiffHunk | null = null;
  let lines: DiffLine[] = [];

  const processHunk = () => {
    if (currentHunk && lines.length > 0) {
      currentHunk.lines = lines;
      currentFile?.hunks.push(currentHunk);
    }
    lines = [];
    currentHunk = null;
  };

  const processFile = () => {
    if (currentFile) {
      processHunk();
      files.push(currentFile);
    }
    currentFile = null;
  };

  const diffLines = diffText.split('\n');
  let i = 0;

  while (i < diffLines.length) {
    const line = diffLines[i];

    // Check for file header
    if (line.startsWith('diff --git')) {
      processFile();
      const match = line.match(/^diff --git a\/(.*) b\/(.*)$/);
      if (match) {
        currentFile = {
          path: match[2],
          oldPath: match[1] !== match[2] ? match[1] : undefined,
          status: 'modified',
          additions: 0,
          deletions: 0,
          hunks: [],
        };
      }
      i++;
      continue;
    }

    // Check for new file mode
    if (line.startsWith('new file mode')) {
      if (currentFile) currentFile.status = 'added';
      i++;
      continue;
    }

    // Check for deleted file mode
    if (line.startsWith('deleted file mode')) {
      if (currentFile) currentFile.status = 'deleted';
      i++;
      continue;
    }

    // Check for hunk header
    const hunkMatch = line.match(hunkRegex);
    if (hunkMatch) {
      processHunk();
      currentHunk = {
        oldStart: parseInt(hunkMatch[1], 10),
        oldLines: parseInt(hunkMatch[2] || '1', 10),
        newStart: parseInt(hunkMatch[3], 10),
        newLines: parseInt(hunkMatch[4] || '1', 10),
        lines: [],
      };
      i++;
      continue;
    }

    // Check for diff line
    const lineMatch = line.match(lineRegex);
    if (lineMatch && currentHunk) {
      const [, prefix, content] = lineMatch;
      const diffLine: DiffLine = {
        type: prefix === '+' ? 'add' : prefix === '-' ? 'delete' : 'context',
        content,
      };

      if (prefix === '+' && currentFile) {
        currentFile.additions++;
      } else if (prefix === '-' && currentFile) {
        currentFile.deletions++;
      }

      lines.push(diffLine);
      i++;
      continue;
    }

    // Mode change (e.g. `chmod +x` on a script): capture the modes so a
    // content-less mode-only diff can be shown as such instead of "binary".
    if (line.startsWith('old mode')) {
      if (currentFile) currentFile.oldMode = line.slice('old mode'.length).trim();
      i++;
      continue;
    }
    if (line.startsWith('new mode')) {
      if (currentFile) currentFile.newMode = line.slice('new mode'.length).trim();
      i++;
      continue;
    }

    // Rename: paths differ, and there may be no content hunks at all.
    if (line.startsWith('rename from') || line.startsWith('rename to')) {
      if (currentFile) currentFile.status = 'renamed';
      i++;
      continue;
    }

    // Skip git internal lines
    if (line.startsWith('index ') || line.startsWith('similarity index')) {
      i++;
      continue;
    }

    // Binary file indicator — the ONLY signal that a file is genuinely
    // non-textual. Distinct from a zero-hunk text diff (mode/rename change).
    if (line.startsWith('Binary files')) {
      if (currentFile) currentFile.isBinary = true;
      i++;
      continue;
    }

    // Empty or unknown line
    i++;
  }

  // Process remaining
  processFile();

  return files;
}
