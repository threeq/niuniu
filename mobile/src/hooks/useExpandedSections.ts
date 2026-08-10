import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ProjectSection } from '../utils/groupWorkspaces';

export interface UseExpandedSectionsResult {
  effectiveExpanded: Set<string>;
  toggle: (key: string) => void;
}

export function useExpandedSections(
  sections: ProjectSection[],
  isSearchActive: boolean,
): UseExpandedSectionsResult {
  const keysSig = sections.map((s) => s.key).join('\0');

  const [expanded, setExpanded] = useState<Set<string>>(
    () => new Set(sections.map((s) => s.key)),
  );
  const prevKeysRef = useRef<Set<string>>(new Set(sections.map((s) => s.key)));

  useEffect(() => {
    const nextKeys = new Set(sections.map((s) => s.key));
    const prev = prevKeysRef.current;
    prevKeysRef.current = nextKeys;

    setExpanded((current) => {
      let changed = false;
      const next = new Set(current);
      // Add any newly-appearing keys (default expanded).
      for (const k of nextKeys) {
        if (!prev.has(k) && !next.has(k)) {
          next.add(k);
          changed = true;
        }
      }
      // Remove any keys that no longer exist as sections.
      for (const k of next) {
        if (!nextKeys.has(k)) {
          next.delete(k);
          changed = true;
        }
      }
      return changed ? next : current;
    });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [keysSig]);

  const allKeysSet = useMemo(
    () => new Set(sections.map((s) => s.key)),
    [keysSig],
  );

  const effectiveExpanded = isSearchActive ? allKeysSet : expanded;

  const toggle = useCallback((key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  return { effectiveExpanded, toggle };
}
