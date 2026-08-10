import { useState } from 'react';
import { ChevronDown, ChevronRight, User, Building2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import { getOwnerColor } from '@/lib/owner-color';

interface Props {
  ownerKey: string;     // e.g. "user:42" or "org:5"
  label: string;        // "Personal" or org name
  icon: 'user' | 'org';
  count: number;
  storageKey: string;   // e.g. "sidebar-owner-projects:user:42"
  children: React.ReactNode;
}

function usePersistedCollapse(storageKey: string, defaultValue = false): [boolean, (v: boolean) => void] {
  const key = `sidebar-collapse-${storageKey}`;
  const [collapsed, setCollapsed] = useState(() => {
    try {
      const stored = localStorage.getItem(key);
      return stored ? JSON.parse(stored) : defaultValue;
    } catch {
      return defaultValue;
    }
  });
  return [
    collapsed,
    (v: boolean) => {
      setCollapsed(v);
      localStorage.setItem(key, JSON.stringify(v));
    },
  ];
}

export function OwnerGroupSection({ ownerKey, label, icon, count, storageKey, children }: Props) {
  const [collapsed, setCollapsed] = usePersistedCollapse(storageKey);
  const Icon = icon === 'user' ? User : Building2;
  const colorClass = getOwnerColor(ownerKey);

  return (
    <div className="mb-2">
      <button
        onClick={() => setCollapsed(!collapsed)}
        className={cn(
          'w-full flex items-center gap-1.5 px-2 py-1 text-xs font-semibold transition-colors hover:opacity-80',
          colorClass
        )}
      >
        {collapsed ? <ChevronRight className="w-3 h-3" /> : <ChevronDown className="w-3 h-3" />}
        <Icon className="w-3 h-3" />
        <span>{label}</span>
        <span className="ml-auto text-muted-foreground font-normal">{count}</span>
      </button>
      {!collapsed && <div className="mt-0.5">{children}</div>}
    </div>
  );
}
