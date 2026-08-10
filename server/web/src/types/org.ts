export interface OwnerRef {
  type: 'user' | 'org';
  id: number;
  name?: string;  // display name, only populated when server returns it on resource objects
  slug?: string;  // only for orgs
}

export interface Org {
  id: number;
  slug: string;
  name: string;
  description: string;
  role?: 'owner' | 'admin' | 'member';  // the calling user's role in this org (present on /api/me/orgs)
  created_at: string;
  updated_at: string;
}

export interface OrgMember {
  org_id: number;
  user_id: number;
  username: string;
  display_name: string;
  role: 'owner' | 'admin' | 'member';
  created_at: string;
}

export interface User {
  id: number;
  username: string;
  display_name: string;
  role: 'admin' | 'member' | 'viewer';
}

export interface OrgAuditEntry {
  id: number;
  org_id: number;
  actor_user_id: number | null;
  actor_label: string;
  action: string;
  target_type: string;
  target_id: number;
  payload: string;  // JSON
  created_at: string;
}

// Minimal projection returned by /api/users/search and consumed by UserSearchInput.
export interface SelectedUser {
  id: number;
  username: string;
  display_name: string;
}
