import { create } from 'zustand';

interface ProjectFilterState {
  selectedProjectId: number | null;
  setProject: (id: number | null) => void;
}

export const useProjectFilterStore = create<ProjectFilterState>((set) => ({
  selectedProjectId: null,
  setProject: (id) => set({ selectedProjectId: id }),
}));
