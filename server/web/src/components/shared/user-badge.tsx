import { User } from 'lucide-react';
import type { OwnerRef } from '@/types/org';

interface Props {
  user: OwnerRef;
  className?: string;
}

export function UserBadge({ user, className }: Props) {
  const label = user.name || `User #${user.id}`;
  return (
    <span
      className={`inline-flex items-center gap-1 text-xs text-muted-foreground ${className ?? ''}`}
    >
      <User className="h-3 w-3" />
      <span className="truncate">{label}</span>
    </span>
  );
}
