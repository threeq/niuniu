import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import type { Project } from '@/types/api';
import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';

// Projects the current caller can WRITE to — the eligible targets for approving
// / reassigning an owner-level chat (design §7/§9: "route to a project" requires
// EnsureOwnerWritable on that project). Mirrors `useIsProjectAdmin` for both
// owner kinds so the dropdown never offers a project the server would reject.
export function useWritableProjects() {
  const myOrgs = useOrgStore((s) => s.myOrgs);
  const me = useAuthStore((s) => s.user);

  // Include hidden projects: routing an IM chat is an admin action and a hidden
  // project is still a valid target (it is only hidden from the main board list,
  // not archived), so the approve/reassign dropdown must offer it too.
  const query = useQuery({
    queryKey: ['projects', 'active,hidden'],
    queryFn: () => api.get<Project[]>('/projects', { params: { status: 'active,hidden' } }),
    retry: 1,
  });

  const writable = useMemo<Project[]>(() => {
    const all = query.data ?? [];
    if (!me) return [];
    return all.filter((p) => {
      if (!p.owner) return false;
      if (p.owner.type === 'user') return p.owner.id === me.id;
      const org = myOrgs.find((o) => o.id === p.owner!.id);
      return org?.role === 'owner' || org?.role === 'admin';
    });
  }, [query.data, me, myOrgs]);

  return { projects: writable, isLoading: query.isLoading };
}
