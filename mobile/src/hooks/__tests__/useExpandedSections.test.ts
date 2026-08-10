import { renderHook, act } from '@testing-library/react-native';
import { useExpandedSections } from '../useExpandedSections';
import type { UseExpandedSectionsResult } from '../useExpandedSections';
import type { ProjectSection } from '../../utils/groupWorkspaces';

function section(key: string): ProjectSection {
  return {
    key,
    title: key,
    workspaces: [],
    stats: { running: 0, needs_review: 0, attention: 0 },
    latestUpdatedAt: '2026-05-01T00:00:00Z',
  };
}

describe('useExpandedSections', () => {
  it('starts with every section key expanded', () => {
    const sections = [section('A'), section('B')];
    const { result } = renderHook(() => useExpandedSections(sections, false));
    expect(result.current.effectiveExpanded.has('A')).toBe(true);
    expect(result.current.effectiveExpanded.has('B')).toBe(true);
  });

  it('toggle removes a previously-expanded key', () => {
    const sections = [section('A'), section('B')];
    const { result } = renderHook(() => useExpandedSections(sections, false));
    act(() => result.current.toggle('A'));
    expect(result.current.effectiveExpanded.has('A')).toBe(false);
    expect(result.current.effectiveExpanded.has('B')).toBe(true);
  });

  it('newly appearing sections default to expanded', () => {
    let sections = [section('A')];
    const { result, rerender } = renderHook<UseExpandedSectionsResult, { s: ProjectSection[] }>(
      ({ s }) => useExpandedSections(s, false),
      { initialProps: { s: sections } },
    );
    act(() => result.current.toggle('A'));
    expect(result.current.effectiveExpanded.has('A')).toBe(false);

    sections = [section('A'), section('B')];
    rerender({ s: sections });
    expect(result.current.effectiveExpanded.has('A')).toBe(false); // user's collapse preserved
    expect(result.current.effectiveExpanded.has('B')).toBe(true);  // new key auto-expanded
  });

  it('disappearing sections are pruned from internal state', () => {
    let sections = [section('A'), section('B')];
    const { result, rerender } = renderHook<UseExpandedSectionsResult, { s: ProjectSection[] }>(
      ({ s }) => useExpandedSections(s, false),
      { initialProps: { s: sections } },
    );
    act(() => result.current.toggle('A'));   // collapse A
    sections = [section('B')];
    rerender({ s: sections });               // A removed
    sections = [section('A'), section('B')]; // A reappears
    rerender({ s: sections });
    expect(result.current.effectiveExpanded.has('A')).toBe(true);  // re-added => expanded
  });

  it('search-active overrides user collapse to render fully expanded', () => {
    const sections = [section('A'), section('B')];
    const { result, rerender } = renderHook<UseExpandedSectionsResult, { s: ProjectSection[]; search: boolean }>(
      ({ s, search }) => useExpandedSections(s, search),
      { initialProps: { s: sections, search: false } },
    );
    act(() => result.current.toggle('A')); // user collapses A
    expect(result.current.effectiveExpanded.has('A')).toBe(false);

    rerender({ s: sections, search: true });
    expect(result.current.effectiveExpanded.has('A')).toBe(true);  // override
    expect(result.current.effectiveExpanded.has('B')).toBe(true);

    rerender({ s: sections, search: false });
    expect(result.current.effectiveExpanded.has('A')).toBe(false); // restored
  });
});
