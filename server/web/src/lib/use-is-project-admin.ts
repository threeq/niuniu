import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';
import type { Project } from '@/types/api';

/**
 * Project administration gate.
 *
 * - Personal projects (`owner.type === 'user'`): the owner user is the only
 *   admin.
 * - Org-owned projects: org `owner` and `admin` roles can manage; plain
 *   `member` cannot.
 *
 * Falls back to `false` when auth state isn't loaded yet — UI then renders
 * the read-only path.
 */
export function useIsProjectAdmin(project: Project | undefined | null): boolean {
  const myOrgs = useOrgStore(s => s.myOrgs);
  const me = useAuthStore(s => s.user);
  if (!me || !project?.owner) return false;
  if (project.owner.type === 'user') return project.owner.id === me.id;
  const org = myOrgs.find(o => o.id === project.owner!.id);
  return org?.role === 'owner' || org?.role === 'admin';
}
