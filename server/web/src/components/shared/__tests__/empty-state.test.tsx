import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { EmptyState } from '../empty-state'

describe('EmptyState', () => {
  it('renders with default icon and message', () => {
    render(<EmptyState title="No items found" />)
    expect(screen.getByText('No items found')).toBeInTheDocument()
  })

  it('renders with custom description', () => {
    render(
      <EmptyState title="No items" description="Try adjusting your filters" />
    )
    expect(screen.getByText('Try adjusting your filters')).toBeInTheDocument()
  })

  it('renders with custom icon', () => {
    render(
      <EmptyState
        title="No repositories"
        icon={<div data-testid="custom-icon">Custom</div>}
      />
    )
    expect(screen.getByTestId('custom-icon')).toBeInTheDocument()
  })

  it('renders with action button', () => {
    render(
      <EmptyState
        title="No items"
        action={<button data-testid="action-btn">Add Item</button>}
      />
    )
    expect(screen.getByTestId('action-btn')).toBeInTheDocument()
  })

  it('renders with list variant (bullet points)', () => {
    render(
      <EmptyState
        title="No items"
        suggestions={['Create a new item', 'Import from file']}
      />
    )
    expect(screen.getByText(/create a new item/i)).toBeInTheDocument()
    expect(screen.getByText(/import from file/i)).toBeInTheDocument()
  })

  it('applies custom className', () => {
    render(<EmptyState title="No items" className="custom-class" />)
    const container = screen.getByTestId('empty-state')
    expect(container).toHaveClass('custom-class')
  })

  it('renders with compact size', () => {
    render(<EmptyState title="No items" size="compact" />)
    const container = screen.getByTestId('empty-state')
    expect(container).toHaveClass('py-8')
  })

  it('renders with default (large) size', () => {
    render(<EmptyState title="No items" size="default" />)
    const container = screen.getByTestId('empty-state')
    expect(container).toHaveClass('py-16')
  })

  it('has appropriate accessibility attributes', () => {
    render(<EmptyState title="No items" />)
    const container = screen.getByTestId('empty-state')
    expect(container).toHaveAttribute('role', 'status')
    expect(container).toHaveAttribute('aria-live', 'polite')
  })

  it('renders without icon when icon is null', () => {
    render(<EmptyState title="No items" icon={null} />)
    const container = screen.getByTestId('empty-state')
    // Should not have any icon elements
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })
})
