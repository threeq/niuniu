import type { ReactNode } from 'react'
import { FolderOpen } from 'lucide-react'
import { cn } from '@/lib/utils'

export interface EmptyStateProps {
  title: string
  description?: string
  icon?: ReactNode
  action?: ReactNode
  suggestions?: string[]
  className?: string
  size?: 'default' | 'compact'
}

const DefaultIcon = <FolderOpen className="h-12 w-12 text-muted-foreground" />

export const EmptyState = ({
  title,
  description,
  icon = DefaultIcon,
  action,
  suggestions,
  className,
  size = 'default',
}: EmptyStateProps) => {
  const sizeClasses = {
    default: 'py-16',
    compact: 'py-8',
  }

  return (
    <div
      data-testid="empty-state"
      className={cn(
        'flex flex-col items-center justify-center text-center px-4',
        sizeClasses[size],
        className
      )}
      role="status"
      aria-live="polite"
    >
      {icon && <div className="mb-4">{icon}</div>}

      <h3 className="text-lg font-semibold mb-2">{title}</h3>

      {description && (
        <p className="text-sm text-muted-foreground mb-4 max-w-sm">{description}</p>
      )}

      {suggestions && suggestions.length > 0 && (
        <ul className="text-sm text-muted-foreground mb-4 space-y-1">
          {suggestions.map((suggestion, index) => (
            <li key={index}>• {suggestion}</li>
          ))}
        </ul>
      )}

      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
