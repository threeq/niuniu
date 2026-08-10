import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { LoadingSkeleton } from '../loading-skeleton'

describe('LoadingSkeleton', () => {
  describe('card variant', () => {
    it('renders card skeleton with correct structure', () => {
      render(<LoadingSkeleton variant="card" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toBeInTheDocument()
      expect(skeleton).toHaveClass('rounded-lg', 'p-4')
    })

    it('renders multiple card skeletons when count is specified', () => {
      render(<LoadingSkeleton variant="card" count={3} />)
      const skeletons = screen.getAllByTestId('loading-skeleton')
      expect(skeletons).toHaveLength(3)
    })

    it('applies custom className', () => {
      render(<LoadingSkeleton variant="card" className="custom-class" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toHaveClass('custom-class')
    })
  })

  describe('list variant', () => {
    it('renders list skeleton with correct structure', () => {
      render(<LoadingSkeleton variant="list" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toBeInTheDocument()
      expect(skeleton).toHaveClass('flex', 'items-center', 'gap-4')
    })

    it('renders avatar placeholder in list variant', () => {
      render(<LoadingSkeleton variant="list" />)
      const avatar = screen.getByTestId('skeleton-avatar')
      expect(avatar).toBeInTheDocument()
      expect(avatar).toHaveClass('rounded-full')
    })

    it('renders content placeholder in list variant', () => {
      render(<LoadingSkeleton variant="list" />)
      const content = screen.getByTestId('skeleton-content')
      expect(content).toBeInTheDocument()
    })
  })

  describe('text variant', () => {
    it('renders text skeleton with correct structure', () => {
      render(<LoadingSkeleton variant="text" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toBeInTheDocument()
      expect(skeleton).toHaveClass('h-4', 'w-full')
    })

    it('renders multiple text lines when count is specified', () => {
      render(<LoadingSkeleton variant="text" count={3} />)
      const lines = screen.getAllByTestId('loading-skeleton')
      expect(lines).toHaveLength(3)
    })

    it('applies custom width to text variant', () => {
      render(<LoadingSkeleton variant="text" width="50%" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toHaveStyle({ width: '50%' })
    })
  })

  describe('accessibility', () => {
    it('has appropriate aria-label for screen readers', () => {
      render(<LoadingSkeleton variant="card" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toHaveAttribute('role', 'status')
      expect(skeleton).toHaveAttribute('aria-label', 'Loading...')
    })

    it('has live region for announcements', () => {
      render(<LoadingSkeleton variant="card" />)
      const skeleton = screen.getByTestId('loading-skeleton')
      expect(skeleton).toHaveAttribute('aria-live', 'polite')
    })
  })
})
