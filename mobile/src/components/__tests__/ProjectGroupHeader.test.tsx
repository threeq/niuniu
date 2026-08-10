import { render, fireEvent } from '@testing-library/react-native';
import { ProjectGroupHeader } from '../ProjectGroupHeader';

const baseStats = { running: 0, needs_review: 0, attention: 0 };

describe('ProjectGroupHeader', () => {
  it('renders title and count', () => {
    const { getByText } = render(
      <ProjectGroupHeader
        sectionKey="P"
        title="移动端优化"
        count={3}
        stats={baseStats}
        isExpanded
        onToggle={jest.fn()}
      />,
    );
    expect(getByText('移动端优化')).toBeTruthy();
    expect(getByText('3')).toBeTruthy();
  });

  it('shows status badges only for non-zero counts', () => {
    const { getByTestId, queryByTestId } = render(
      <ProjectGroupHeader
        sectionKey="P"
        title="P"
        count={4}
        stats={{ running: 1, needs_review: 2, attention: 0 }}
        isExpanded
        onToggle={jest.fn()}
      />,
    );
    expect(getByTestId('group-stat-running')).toBeTruthy();
    expect(getByTestId('group-stat-needs_review')).toBeTruthy();
    expect(queryByTestId('group-stat-attention')).toBeNull();
  });

  it('renders no badges when stats are all zero', () => {
    const { queryByTestId } = render(
      <ProjectGroupHeader
        sectionKey="P"
        title="P"
        count={2}
        stats={baseStats}
        isExpanded
        onToggle={jest.fn()}
      />,
    );
    expect(queryByTestId('group-stat-running')).toBeNull();
    expect(queryByTestId('group-stat-needs_review')).toBeNull();
    expect(queryByTestId('group-stat-attention')).toBeNull();
  });

  it('fires onToggle with sectionKey when pressed', () => {
    const onToggle = jest.fn();
    const { getByTestId } = render(
      <ProjectGroupHeader
        sectionKey="P"
        title="P"
        count={1}
        stats={baseStats}
        isExpanded
        onToggle={onToggle}
      />,
    );
    fireEvent.press(getByTestId('project-group-header'));
    expect(onToggle).toHaveBeenCalledWith('P');
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it('exposes isExpanded via accessibilityState for a11y / chevron rendering', () => {
    const { getByTestId } = render(
      <ProjectGroupHeader
        sectionKey="P"
        title="P"
        count={0}
        stats={baseStats}
        isExpanded={false}
        onToggle={jest.fn()}
      />,
    );
    const node = getByTestId('project-group-header');
    expect(node.props.accessibilityState).toEqual({ expanded: false });
    // Chevron icon name reflects isExpanded={false}
    const chevron = node.findByProps({ name: 'chevron-forward' });
    expect(chevron).toBeTruthy();
  });
});
