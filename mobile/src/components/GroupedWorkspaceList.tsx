import React, { useCallback } from 'react';
import { SectionList, StyleSheet, View, type RefreshControlProps } from 'react-native';
import type { Workspace } from '../api/types';
import type { ProjectSection } from '../utils/groupWorkspaces';
import { useExpandedSections } from '../hooks/useExpandedSections';
import { spacing } from '../theme/tokens';
import { ProjectGroupHeader } from './ProjectGroupHeader';
import { WorkspaceCard } from './WorkspaceCard';

interface GroupedWorkspaceListProps {
  sections: ProjectSection[];
  isSearchActive: boolean;
  onWorkspacePress: (ws: Workspace) => void;
  refreshControl?: React.ReactElement<RefreshControlProps>;
  ListHeaderComponent?: React.ReactElement | null;
  ListEmptyComponent?: React.ReactElement | null;
}

export function GroupedWorkspaceList({
  sections,
  isSearchActive,
  onWorkspacePress,
  refreshControl,
  ListHeaderComponent,
  ListEmptyComponent,
}: GroupedWorkspaceListProps) {
  const { effectiveExpanded, toggle } = useExpandedSections(
    sections,
    isSearchActive,
  );

  const sectionListData = sections.map((s) => ({
    key: s.key,
    title: s.title,
    data: effectiveExpanded.has(s.key) ? s.workspaces : [],
    section: s,
  }));

  const renderSectionHeader = useCallback(
    ({ section }: { section: typeof sectionListData[number] }) => {
      const sec = section.section as ProjectSection;
      return (
        <ProjectGroupHeader
          sectionKey={sec.key}
          title={sec.title}
          count={sec.workspaces.length}
          stats={sec.stats}
          isExpanded={effectiveExpanded.has(sec.key)}
          onToggle={toggle}
        />
      );
    },
    [effectiveExpanded, toggle],
  );

  const renderItem = useCallback(
    ({ item }: { item: Workspace }) => (
      <View style={styles.cardWrap}>
        <WorkspaceCard ws={item} onPress={() => onWorkspacePress(item)} />
      </View>
    ),
    [onWorkspacePress],
  );

  return (
    <SectionList
      sections={sectionListData}
      keyExtractor={(item, index) => `${item.id}-${index}`}
      renderSectionHeader={renderSectionHeader}
      renderItem={renderItem}
      ItemSeparatorComponent={() => <View style={{ height: spacing.sm }} />}
      contentContainerStyle={styles.listContent}
      stickySectionHeadersEnabled={false}
      keyboardDismissMode="on-drag"
      showsVerticalScrollIndicator={false}
      refreshControl={refreshControl}
      ListHeaderComponent={ListHeaderComponent ?? null}
      ListEmptyComponent={sections.length === 0 ? (ListEmptyComponent ?? null) : null}
    />
  );
}

const styles = StyleSheet.create({
  listContent: {
    paddingBottom: spacing['3xl'],
  },
  cardWrap: {
    paddingHorizontal: spacing.lg,
  },
});
