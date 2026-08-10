import React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const skeletonVariants = cva(
  'animate-pulse bg-muted rounded-md',
  {
    variants: {
      variant: {
        card: 'rounded-lg p-4',
        list: 'flex items-center gap-4',
        text: 'h-4 w-full',
      },
    },
    defaultVariants: {
      variant: 'card',
    },
  }
)

export interface LoadingSkeletonProps
  extends VariantProps<typeof skeletonVariants> {
  count?: number
  className?: string
  width?: string
}

const SkeletonBase = ({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) => {
  return <div className={cn('animate-pulse bg-muted rounded-md', className)} {...props} />
}

export const LoadingSkeleton = ({
  variant = 'card',
  count = 1,
  className,
  width,
}: LoadingSkeletonProps) => {
  const skeletons = Array.from({ length: count }, (_, i) => i)

  if (variant === 'card') {
    return (
      <>
        {skeletons.map((i) => (
          <div
            key={i}
            data-testid="loading-skeleton"
            className={cn(skeletonVariants({ variant }), className)}
            role="status"
            aria-label="Loading..."
            aria-live="polite"
          >
            <div className="space-y-3">
              <SkeletonBase className="h-4 w-3/4" />
              <SkeletonBase className="h-3 w-1/2" />
              <SkeletonBase className="h-3 w-5/6" />
            </div>
          </div>
        ))}
      </>
    )
  }

  if (variant === 'list') {
    return (
      <>
        {skeletons.map((i) => (
          <div
            key={i}
            data-testid="loading-skeleton"
            className={cn(skeletonVariants({ variant }), className)}
            role="status"
            aria-label="Loading..."
            aria-live="polite"
          >
            <SkeletonBase
              data-testid="skeleton-avatar"
              className="h-12 w-12 rounded-full flex-shrink-0"
            />
            <div data-testid="skeleton-content" className="flex-1 space-y-2">
              <SkeletonBase className="h-4 w-3/4" />
              <SkeletonBase className="h-3 w-1/2" />
            </div>
          </div>
        ))}
      </>
    )
  }

  if (variant === 'text') {
    return (
      <>
        {skeletons.map((i) => (
          <div
            key={i}
            data-testid="loading-skeleton"
            className={cn(skeletonVariants({ variant }), className)}
            role="status"
            aria-label="Loading..."
            aria-live="polite"
            style={width ? { width } : undefined}
          />
        ))}
      </>
    )
  }

  return null
}
