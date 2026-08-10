import { create } from 'zustand';
import { api } from '@/lib/api';
import type { Org } from '@/types/org';

// Single responsibility: hold the list of orgs the caller belongs to.
// The caller's identity (id, username, role) lives in auth-store and is
// authoritative — duplicating "currentUser" here is what made the "组织"
// admin tab disappear after login until org-store happened to refetch.
interface OrgState {
  myOrgs: Org[];
  loaded: boolean;
  fetch: () => Promise<void>;
  invalidate: () => void;
  // allOrgs holds EVERY org on the deployment and is populated only for global
  // admins via fetchAll(). Kept separate from myOrgs on purpose: myOrgs drives
  // owner-scoped authorization (OwnerPicker, "create resource under org"), so
  // an admin's read-only visibility of foreign orgs must never leak into it.
  allOrgs: Org[];
  fetchAll: () => Promise<void>;
}

export const useOrgStore = create<OrgState>((set) => ({
  myOrgs: [],
  loaded: false,
  fetch: async () => {
    try {
      const orgs = await api.listMyOrgs();
      set({ myOrgs: orgs, loaded: true });
    } catch {
      // Not authenticated or server unreachable — leave empty state.
      set({ myOrgs: [], loaded: true });
    }
  },
  invalidate: () => set({ loaded: false }),
  allOrgs: [],
  fetchAll: async () => {
    try {
      const orgs = await api.listAllOrgs();
      set({ allOrgs: orgs });
    } catch {
      // Not a global admin, or server unreachable — leave empty.
      set({ allOrgs: [] });
    }
  },
}));
