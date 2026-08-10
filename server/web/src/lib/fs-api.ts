import { api } from './api';

// Read-only local-directory browser (personal edition only) backing the
// knowledge-base folder picker. The browser can't expose absolute paths, so the
// picker walks the local server's filesystem.

export interface FsDir {
  name: string;
  path: string;
}

export interface ListDirsResponse {
  path: string;
  parent: string; // "" at the filesystem root
  home: string;
  dirs: FsDir[];
}

/** List immediate subdirectories of `path` (defaults to the user's home). */
export async function listDirs(path?: string): Promise<ListDirsResponse> {
  const q = path ? `?path=${encodeURIComponent(path)}` : '';
  return api.get<ListDirsResponse>(`/fs/list-dirs${q}`);
}
