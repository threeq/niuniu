import { getAccessToken } from '@/stores/auth-store';

// getRepoFileContentUrl builds the raw-bytes URL for a file in a repository's
// default-branch tree, carrying the auth token as a query param (the endpoint is
// hit by <img>/<video>/<iframe>/fetch which can't set an Authorization header).
// Mirrors getFileContentUrl for workspaces so the shared FilePreviewByUrl
// renderer works against repository files too.
export function getRepoFileContentUrl(repoId: string, path: string): string {
  const params = new URLSearchParams({ path, mode: 'raw' });
  const token = getAccessToken();
  if (token) params.set('token', token);
  return `/api/repositories/${repoId}/files/content?${params.toString()}`;
}
