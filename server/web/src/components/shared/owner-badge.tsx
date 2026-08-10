import { User, Building2 } from 'lucide-react';
import { useOrgStore } from '@/stores/org-store';
import { useAuthStore } from '@/stores/auth-store';
import type { OwnerRef } from '@/types/org';

interface Props {
  owner: OwnerRef;
  className?: string;
}

export function OwnerBadge({ owner, className }: Props) {
  const currentUser = useAuthStore((s) => s.user);
  const myOrgs = useOrgStore((s) => s.myOrgs);

  // Hide entirely when user has no orgs.
  if (!currentUser || myOrgs.length === 0) return null;

  // When the user has exactly one org and the resource belongs to that org,
  // the badge is uninformative (every resource is in "the only org").
  if (myOrgs.length === 1 && owner.type === 'org' && owner.id === myOrgs[0].id) return null;

  let label = '';
  let Icon: React.ElementType;
  if (owner.type === 'user') {
    Icon = User;
    label = currentUser && owner.id === currentUser.id ? 'Personal' : (owner.name || `User #${owner.id}`);
  } else {
    Icon = Building2;
    label = owner.name || owner.slug || `Org #${owner.id}`;
  }

  return (
    <span className={`inline-flex items-center gap-1 text-xs text-muted-foreground ${className ?? ''}`}>
      <Icon className="h-3 w-3" />
      <span>{label}</span>
    </span>
  );
}
