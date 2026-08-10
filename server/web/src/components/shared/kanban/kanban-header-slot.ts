import { createContext } from 'react';

// The project detail page renders a single 40px header row that holds the
// 看板/记忆/设置 tabs plus a right-hand slot element. KanbanBoard portals its
// toolbar (project name + stats + search/filter + add) into this slot so the
// tabs and the board toolbar share ONE row instead of stacking into two.
//
// `null` (the default, i.e. no provider — e.g. any standalone KanbanBoard use)
// makes KanbanBoard fall back to rendering its toolbar inline as its own row.
export const KanbanHeaderSlotContext = createContext<HTMLElement | null>(null);
