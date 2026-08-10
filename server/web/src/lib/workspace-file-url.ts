import { getAccessToken } from '@/stores/auth-store';

// getFileContentUrl builds the workspace file-content endpoint URL, carrying the
// auth token as a query param (the endpoint is hit by <img>/<audio>/<iframe>/fetch
// which can't set an Authorization header). mode='raw' serves the raw bytes with
// a proper Content-Type; omit mode for the JSON preview response.
//
// Shared by attachment-preview and file-preview so the URL/token construction
// lives in one place (was duplicated as a file-local helper in each).
export function getFileContentUrl(workspaceId: string, path: string, mode?: string): string {
  const params = new URLSearchParams({ path });
  if (mode) params.set('mode', mode);
  const token = getAccessToken();
  if (token) params.set('token', token);
  return `/api/workspaces/${workspaceId}/file-content?${params.toString()}`;
}
