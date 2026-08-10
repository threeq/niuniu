import React, { useCallback, useEffect, useState } from 'react';
import {
  ActivityIndicator,
  FlatList,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { Stack, useLocalSearchParams, useRouter } from 'expo-router';
import Ionicons from '@expo/vector-icons/Ionicons';
import { api } from '../../src/api/client';
import type {
  BranchInfo,
  ChangeEntry,
  ChangeStatus,
  CommitEntry,
  FileEntry,
  GitStatusResponse,
  RepositoryDetail,
  WorktreeEntry,
} from '../../src/api/types';
import { SegmentedControl } from '../../src/components/SegmentedControl';
import { useThemeColors } from '../../src/theme/useTheme';
import { radius, spacing, typography } from '../../src/theme/tokens';
import { formatRelativeTime } from '../../src/utils/formatTime';

// ─── Constants ───────────────────────────────────────────────────────────────

const TAB_OPTIONS_BASE = ['文件', '分支', '提交', 'Worktree'];
const CHANGES_TAB_LABEL = '变更';

// ─── File Tree Tab ────────────────────────────────────────────────────────────

// Layered/lazy file listing: only the root is fetched up front; each directory
// loads its own children on first expand via /repositories/:id/files?path=…
// The full recursive tree returned by /detail is intentionally NOT used here
// — it walks the entire fs and times out on large repos.
function FileTreeView({ repoId }: { repoId: string }) {
  const colors = useThemeColors();
  const [childrenByPath, setChildrenByPath] = useState<Map<string, FileEntry[]>>(
    () => new Map(),
  );
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => new Set());
  const [loadingPaths, setLoadingPaths] = useState<Set<string>>(() => new Set());
  const [errorPaths, setErrorPaths] = useState<Set<string>>(() => new Set());

  const loadPath = useCallback(
    async (path: string) => {
      setLoadingPaths((prev) => {
        if (prev.has(path)) return prev;
        const next = new Set(prev);
        next.add(path);
        return next;
      });
      setErrorPaths((prev) => {
        if (!prev.has(path)) return prev;
        const next = new Set(prev);
        next.delete(path);
        return next;
      });
      try {
        const url = path
          ? `/repositories/${repoId}/files?path=${encodeURIComponent(path)}`
          : `/repositories/${repoId}/files`;
        const data = await api.get<FileEntry[]>(url);
        setChildrenByPath((prev) => {
          const next = new Map(prev);
          next.set(path, data ?? []);
          return next;
        });
      } catch {
        setErrorPaths((prev) => {
          const next = new Set(prev);
          next.add(path);
          return next;
        });
      } finally {
        setLoadingPaths((prev) => {
          if (!prev.has(path)) return prev;
          const next = new Set(prev);
          next.delete(path);
          return next;
        });
      }
    },
    [repoId],
  );

  useEffect(() => {
    loadPath('');
  }, [loadPath]);

  const handleDirPress = useCallback(
    (path: string) => {
      const wasExpanded = expandedPaths.has(path);
      setExpandedPaths((prev) => {
        const next = new Set(prev);
        if (wasExpanded) next.delete(path);
        else next.add(path);
        return next;
      });
      if (
        !wasExpanded &&
        !childrenByPath.has(path) &&
        !loadingPaths.has(path)
      ) {
        loadPath(path);
      }
    },
    [expandedPaths, childrenByPath, loadingPaths, loadPath],
  );

  const renderEntry = (entry: FileEntry, depth: number): React.ReactNode => {
    const isDir = entry.type === 'dir';
    const isExpanded = isDir && expandedPaths.has(entry.path);
    const isLoading = isDir && loadingPaths.has(entry.path);
    const isError = isDir && errorPaths.has(entry.path);
    const children = isDir ? childrenByPath.get(entry.path) : undefined;
    const iconName = isDir
      ? isExpanded
        ? 'folder-open-outline'
        : 'folder-outline'
      : 'document-outline';
    return (
      <React.Fragment key={entry.path}>
        <Pressable
          style={[
            styles.treeRow,
            { paddingLeft: spacing.lg + depth * spacing.lg },
          ]}
          onPress={() => isDir && handleDirPress(entry.path)}
        >
          <Ionicons
            name={iconName}
            size={16}
            color={isDir ? colors.brand.accent : colors.text.secondary}
            style={styles.treeIcon}
          />
          <Text
            style={[
              styles.treeName,
              {
                color: isDir ? colors.text.primary : colors.text.secondary,
              },
              isDir && { fontWeight: '500' },
            ]}
            numberOfLines={1}
          >
            {entry.name}
          </Text>
          {isDir &&
            (isLoading ? (
              <ActivityIndicator size="small" color={colors.text.tertiary} />
            ) : (
              <Ionicons
                name={isExpanded ? 'chevron-down' : 'chevron-forward'}
                size={14}
                color={colors.text.tertiary}
              />
            ))}
        </Pressable>
        {isDir && isExpanded ? (
          isError && !children ? (
            <Pressable
              style={[
                styles.treeRow,
                { paddingLeft: spacing.lg + (depth + 1) * spacing.lg },
              ]}
              onPress={() => loadPath(entry.path)}
            >
              <Text
                style={[styles.treeName, { color: colors.status.error }]}
                numberOfLines={1}
              >
                加载失败，点击重试
              </Text>
            </Pressable>
          ) : children && children.length === 0 ? (
            <View
              style={[
                styles.treeRow,
                { paddingLeft: spacing.lg + (depth + 1) * spacing.lg },
              ]}
            >
              <Text
                style={[styles.treeName, { color: colors.text.tertiary }]}
                numberOfLines={1}
              >
                空目录
              </Text>
            </View>
          ) : (
            children?.map((child) => renderEntry(child, depth + 1))
          )
        ) : null}
      </React.Fragment>
    );
  };

  const rootEntries = childrenByPath.get('');
  const rootLoading = loadingPaths.has('');
  const rootError = errorPaths.has('');

  if (rootLoading && !rootEntries) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.brand.accent} />
      </View>
    );
  }

  if (rootError && !rootEntries) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.errorText, { color: colors.status.error }]}>
          加载文件失败
        </Text>
        <Pressable onPress={() => loadPath('')} style={styles.retryBtn}>
          <Text style={[styles.retryText, { color: colors.brand.accent }]}>
            重试
          </Text>
        </Pressable>
      </View>
    );
  }

  if (rootEntries && rootEntries.length === 0) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
          仓库为空
        </Text>
      </View>
    );
  }

  return (
    <ScrollView
      style={styles.tabContent}
      showsVerticalScrollIndicator={false}
      contentContainerStyle={styles.treeContent}
    >
      {rootEntries?.map((entry) => renderEntry(entry, 0))}
    </ScrollView>
  );
}

// ─── Changes Tab ──────────────────────────────────────────────────────────────

const CHANGE_STATUS_ORDER: Record<ChangeStatus, number> = {
  A: 0,
  M: 1,
  D: 2,
  '?': 3,
};

function flattenStatus(status: GitStatusResponse): ChangeEntry[] {
  const list: ChangeEntry[] = [];
  status.added?.forEach((p) => list.push({ path: p, status: 'A' }));
  status.modified?.forEach((p) => list.push({ path: p, status: 'M' }));
  status.deleted?.forEach((p) => list.push({ path: p, status: 'D' }));
  status.untracked?.forEach((p) => list.push({ path: p, status: '?' }));
  list.sort((a, b) => {
    const so = CHANGE_STATUS_ORDER[a.status] - CHANGE_STATUS_ORDER[b.status];
    if (so !== 0) return so;
    return a.path.localeCompare(b.path);
  });
  return list;
}

function ChangesView({ repoId }: { repoId: string }) {
  const colors = useThemeColors();
  const [entries, setEntries] = useState<ChangeEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const data = await api.get<GitStatusResponse>(
          `/repositories/${repoId}/git/status`,
        );
        setEntries(data ? flattenStatus(data) : []);
      } catch {
        setError(true);
      } finally {
        setLoading(false);
      }
    })();
  }, [repoId]);

  const statusStyle = (s: ChangeStatus) => {
    switch (s) {
      case 'A':
        return { bg: colors.status.successBg, fg: colors.status.success };
      case 'M':
        return { bg: colors.status.warningBg, fg: colors.status.warning };
      case 'D':
        return { bg: colors.status.errorBg, fg: colors.status.error };
      case '?':
      default:
        return { bg: colors.bg.muted, fg: colors.text.tertiary };
    }
  };

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.brand.accent} />
      </View>
    );
  }

  if (error) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.errorText, { color: colors.status.error }]}>
          加载变更失败
        </Text>
      </View>
    );
  }

  if (entries.length === 0) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
          暂无变更
        </Text>
      </View>
    );
  }

  return (
    <FlatList
      data={entries}
      keyExtractor={(item) => `${item.status}:${item.path}`}
      contentContainerStyle={styles.listContent}
      showsVerticalScrollIndicator={false}
      renderItem={({ item }) => {
        const sStyle = statusStyle(item.status);
        return (
          <View
            style={[
              styles.changeRow,
              {
                backgroundColor: colors.bg.surface,
                borderColor: colors.border.default,
              },
            ]}
          >
            <View
              style={[styles.statusBadge, { backgroundColor: sStyle.bg }]}
            >
              <Text style={[styles.statusBadgeText, { color: sStyle.fg }]}>
                {item.status}
              </Text>
            </View>
            <Text
              style={[styles.changePath, { color: colors.text.primary }]}
              numberOfLines={3}
              selectable
            >
              {item.path}
            </Text>
          </View>
        );
      }}
    />
  );
}

// ─── Branches Tab ─────────────────────────────────────────────────────────────

function BranchesView({ repoId }: { repoId: string }) {
  const colors = useThemeColors();
  const [branchInfo, setBranchInfo] = useState<BranchInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const data = await api.get<BranchInfo>(`/repositories/${repoId}/branches`);
        setBranchInfo(data ?? null);
      } catch {
        setError(true);
      } finally {
        setLoading(false);
      }
    })();
  }, [repoId]);

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.brand.accent} />
      </View>
    );
  }

  if (error || !branchInfo) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.errorText, { color: colors.status.error }]}>
          加载分支失败
        </Text>
      </View>
    );
  }

  const { branches, current_branch } = branchInfo;

  if (branches.length === 0) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
          暂无分支
        </Text>
      </View>
    );
  }

  return (
    <FlatList
      data={branches}
      keyExtractor={(item) => item}
      contentContainerStyle={styles.listContent}
      showsVerticalScrollIndicator={false}
      renderItem={({ item }) => {
        const isCurrent = item === current_branch;
        return (
          <View
            style={[
              styles.branchRow,
              {
                backgroundColor: isCurrent
                  ? colors.brand.accentLight
                  : colors.bg.surface,
                borderColor: isCurrent
                  ? colors.brand.accent + '40'
                  : colors.border.default,
              },
            ]}
          >
            <Ionicons
              name={isCurrent ? 'git-branch' : 'git-branch-outline'}
              size={18}
              color={isCurrent ? colors.brand.accent : colors.text.secondary}
              style={styles.branchIcon}
            />
            <Text
              style={[
                styles.branchName,
                {
                  color: isCurrent ? colors.brand.accent : colors.text.primary,
                  fontWeight: isCurrent ? '600' : '400',
                },
              ]}
              numberOfLines={1}
            >
              {item}
            </Text>
            {isCurrent && (
              <View
                style={[
                  styles.currentBadge,
                  { backgroundColor: colors.brand.accent },
                ]}
              >
                <Text style={styles.currentBadgeText}>当前</Text>
              </View>
            )}
          </View>
        );
      }}
    />
  );
}

// ─── Commits Tab ─────────────────────────────────────────────────────────────

function CommitsView({ repoId }: { repoId: string }) {
  const colors = useThemeColors();
  const [commits, setCommits] = useState<CommitEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const data = await api.get<CommitEntry[]>(`/repositories/${repoId}/commits`);
        setCommits(data ?? []);
      } catch {
        setError(true);
      } finally {
        setLoading(false);
      }
    })();
  }, [repoId]);

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.brand.accent} />
      </View>
    );
  }

  if (error) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.errorText, { color: colors.status.error }]}>
          加载提交记录失败
        </Text>
      </View>
    );
  }

  if (commits.length === 0) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
          暂无提交记录
        </Text>
      </View>
    );
  }

  return (
    <FlatList
      data={commits}
      keyExtractor={(item) => item.hash}
      contentContainerStyle={styles.listContent}
      showsVerticalScrollIndicator={false}
      renderItem={({ item }) => (
        <View
          style={[
            styles.commitRow,
            {
              backgroundColor: colors.bg.surface,
              borderColor: colors.border.default,
            },
          ]}
        >
          <View style={styles.commitTop}>
            <View
              style={[
                styles.hashBadge,
                { backgroundColor: colors.bg.muted },
              ]}
            >
              <Text
                style={[styles.commitHash, { color: colors.brand.accent }]}
              >
                {item.hash.slice(0, 7)}
              </Text>
            </View>
            <Text style={[styles.commitTime, { color: colors.text.tertiary }]}>
              {formatRelativeTime(item.date)}
            </Text>
          </View>
          <Text
            style={[styles.commitMessage, { color: colors.text.primary }]}
            numberOfLines={2}
          >
            {item.message}
          </Text>
          <Text style={[styles.commitAuthor, { color: colors.text.secondary }]}>
            {item.author}
          </Text>
        </View>
      )}
    />
  );
}

// ─── Worktree Tab ─────────────────────────────────────────────────────────────

function WorktreeView({ repoId }: { repoId: string }) {
  const colors = useThemeColors();
  const [worktrees, setWorktrees] = useState<WorktreeEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const data = await api.get<WorktreeEntry[]>(
          `/repositories/${repoId}/worktrees`,
        );
        setWorktrees(data ?? []);
      } catch {
        setError(true);
      } finally {
        setLoading(false);
      }
    })();
  }, [repoId]);

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator color={colors.brand.accent} />
      </View>
    );
  }

  if (error) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.errorText, { color: colors.status.error }]}>
          加载 Worktree 失败
        </Text>
      </View>
    );
  }

  if (worktrees.length === 0) {
    return (
      <View style={styles.centered}>
        <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
          暂无 Worktree
        </Text>
      </View>
    );
  }

  return (
    <FlatList
      data={worktrees}
      keyExtractor={(item, index) => item.path ?? String(index)}
      contentContainerStyle={styles.listContent}
      showsVerticalScrollIndicator={false}
      renderItem={({ item }) => (
        <View
          style={[
            styles.worktreeRow,
            {
              backgroundColor: colors.bg.surface,
              borderColor: colors.border.default,
            },
          ]}
        >
          <View style={styles.worktreeTop}>
            <Ionicons
              name="git-branch-outline"
              size={16}
              color={colors.brand.accent}
            />
            <Text
              style={[styles.worktreeBranch, { color: colors.text.primary }]}
              numberOfLines={1}
            >
              {item.branch}
            </Text>
            {item.has_changes && (
              <View
                style={[
                  styles.changesBadge,
                  { backgroundColor: colors.status.warningBg },
                ]}
              >
                <Text
                  style={[
                    styles.changesBadgeText,
                    { color: colors.status.warning },
                  ]}
                >
                  有变更
                </Text>
              </View>
            )}
          </View>
          <Text
            style={[styles.worktreePath, { color: colors.text.secondary }]}
            numberOfLines={1}
          >
            {item.path}
          </Text>
          {item.workspace_id && (
            <View style={styles.worktreeWorkspaceRow}>
              <Ionicons
                name="cube-outline"
                size={13}
                color={colors.text.tertiary}
              />
              <Text
                style={[
                  styles.worktreeWorkspaceText,
                  { color: colors.text.tertiary },
                ]}
                numberOfLines={1}
              >
                工作空间 #{item.workspace_id}
              </Text>
            </View>
          )}
        </View>
      )}
    />
  );
}

// ─── Main Screen ──────────────────────────────────────────────────────────────

export default function RepositoryDetailScreen() {
  const colors = useThemeColors();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const { id } = useLocalSearchParams<{ id: string }>();

  const [detail, setDetail] = useState<RepositoryDetail | null>(null);
  const [tabIndex, setTabIndex] = useState(0);
  // Tabs stay mounted after first visit so switching back doesn't refire the
  // tab's fetch. Index 0 is mounted on first render. Note: BranchesView keeps
  // its own /branches fetch (cannot reuse detail.git.branches because the
  // /branches endpoint excludes branches checked out in worktrees, while
  // detail returns ALL local branches — different semantics).
  const [visitedTabs, setVisitedTabs] = useState<Set<number>>(
    () => new Set([0]),
  );
  const handleTabSelect = useCallback((idx: number) => {
    setTabIndex(idx);
    setVisitedTabs((prev) => {
      if (prev.has(idx)) return prev;
      const next = new Set(prev);
      next.add(idx);
      return next;
    });
  }, []);

  // Fetch repo metadata (name + git status) for the header and to decide whether
  // to surface the 变更 tab. The file tree is loaded lazily by FileTreeView via
  // /repositories/:id/files; the heavy `files.tree` field on /detail is unused.
  useEffect(() => {
    (async () => {
      try {
        const data = await api.get<RepositoryDetail>(
          `/repositories/${id}/detail`,
        );
        if (data) setDetail(data);
      } catch {
        // header degrades gracefully — the file/changes tabs fetch independently
      }
    })();
  }, [id]);

  const title = detail?.name ?? `仓库 #${id}`;
  const git = detail?.git;
  const showChangesTab = !!git?.has_changes;
  const tabOptions = showChangesTab
    ? [...TAB_OPTIONS_BASE, CHANGES_TAB_LABEL]
    : TAB_OPTIONS_BASE;
  const changesTabIndex = TAB_OPTIONS_BASE.length;

  // If the changes tab disappears (e.g. after a refresh shows no changes) and
  // it's currently selected, fall back to the files tab.
  useEffect(() => {
    if (!showChangesTab && tabIndex === changesTabIndex) {
      setTabIndex(0);
    }
  }, [showChangesTab, tabIndex, changesTabIndex]);

  return (
    <View
      style={[
        styles.safeArea,
        { backgroundColor: colors.bg.base, paddingTop: insets.top },
      ]}
    >
      {/* Hide the parent Stack's native header — its registration in
          app/_layout.tsx sets headerShown:true for "repository", which
          would otherwise stack on top of our custom header. */}
      <Stack.Screen options={{ headerShown: false }} />

      {/* Header */}
      <View
        style={[
          styles.header,
          { borderBottomColor: colors.border.default },
        ]}
      >
        <TouchableOpacity
          onPress={() => router.back()}
          hitSlop={8}
          style={styles.backBtn}
        >
          <Ionicons name="chevron-back" size={24} color={colors.text.primary} />
        </TouchableOpacity>
        <View style={styles.titleSlot}>
          <Text
            style={[styles.headerTitle, { color: colors.text.primary }]}
            numberOfLines={1}
          >
            {title}
          </Text>
          {git ? (
            // has_changes reflects the bare repository at repo.path, NOT
            // attached worktrees — those carry their own has_changes flags
            // surfaced inside the Worktree tab.
            <View style={styles.statusRow}>
              <Ionicons
                name="git-branch-outline"
                size={12}
                color={colors.text.tertiary}
              />
              <Text
                style={[styles.statusBranch, { color: colors.text.tertiary }]}
                numberOfLines={1}
              >
                {git.current_branch || '—'}
              </Text>
              <View
                style={[
                  styles.statusDot,
                  {
                    backgroundColor: git.has_changes
                      ? colors.status.warning
                      : colors.status.success,
                  },
                ]}
              />
              <Text
                style={[
                  styles.statusText,
                  {
                    color: git.has_changes
                      ? colors.status.warning
                      : colors.text.tertiary,
                  },
                ]}
                numberOfLines={1}
              >
                {git.has_changes ? '有变更' : '无变更'}
              </Text>
            </View>
          ) : null}
        </View>
        <View style={styles.backBtn} />
      </View>

      {/* Segmented control */}
      <View style={[styles.tabBar, { borderBottomColor: colors.border.default }]}>
        <SegmentedControl
          options={tabOptions}
          selected={tabIndex}
          onSelect={handleTabSelect}
        />
      </View>

      {/* Tab content — visited tabs stay mounted so re-selecting a tab
          doesn't re-fire its fetch. Inactive tabs are hidden via display. */}
      <View style={styles.body}>
        {visitedTabs.has(0) && (
          <View style={tabIndex === 0 ? styles.tabPanel : styles.tabPanelHidden}>
            <FileTreeView repoId={id} />
          </View>
        )}
        {visitedTabs.has(1) && (
          <View style={tabIndex === 1 ? styles.tabPanel : styles.tabPanelHidden}>
            <BranchesView repoId={id} />
          </View>
        )}
        {visitedTabs.has(2) && (
          <View style={tabIndex === 2 ? styles.tabPanel : styles.tabPanelHidden}>
            <CommitsView repoId={id} />
          </View>
        )}
        {visitedTabs.has(3) && (
          <View style={tabIndex === 3 ? styles.tabPanel : styles.tabPanelHidden}>
            <WorktreeView repoId={id} />
          </View>
        )}
        {showChangesTab && visitedTabs.has(changesTabIndex) && (
          <View
            style={
              tabIndex === changesTabIndex ? styles.tabPanel : styles.tabPanelHidden
            }
          >
            <ChangesView repoId={id} />
          </View>
        )}
      </View>
    </View>
  );
}

// ─── Styles ──────────────────────────────────────────────────────────────────

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
  },

  // ─── Header ──────────────────────────────────────────────────────
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.md,
    borderBottomWidth: 1,
  },
  backBtn: {
    width: 40,
    alignItems: 'center',
    justifyContent: 'center',
  },
  titleSlot: {
    flex: 1,
    alignItems: 'center',
    minWidth: 0,
  },
  headerTitle: {
    ...typography.sectionHead,
    textAlign: 'center',
  },
  statusRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    marginTop: 2,
    overflow: 'hidden',
  },
  statusBranch: {
    ...typography.caption,
    flexShrink: 1,
    maxWidth: 120,
  },
  statusDot: {
    width: 6,
    height: 6,
    borderRadius: 3,
    marginLeft: 4,
    flexShrink: 0,
  },
  statusText: {
    ...typography.caption,
    flexShrink: 0,
  },

  // ─── Tab bar ─────────────────────────────────────────────────────
  tabBar: {
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.sm,
    borderBottomWidth: 1,
  },

  // ─── Body ────────────────────────────────────────────────────────
  body: {
    flex: 1,
  },
  tabContent: {
    flex: 1,
  },
  tabPanel: {
    flex: 1,
  },
  tabPanelHidden: {
    display: 'none',
  },

  // ─── Shared ──────────────────────────────────────────────────────
  centered: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingTop: 60,
  },
  emptyText: {
    ...typography.body,
  },
  errorText: {
    ...typography.body,
  },
  listContent: {
    paddingHorizontal: spacing.lg,
    paddingTop: spacing.md,
    paddingBottom: spacing['3xl'],
    gap: spacing.sm,
  },

  // ─── File tree ───────────────────────────────────────────────────
  treeContent: {
    paddingTop: spacing.sm,
    paddingBottom: spacing['3xl'],
  },
  treeRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: spacing.sm,
    paddingRight: spacing.lg,
  },
  treeIcon: {
    marginRight: spacing.sm,
  },
  treeName: {
    ...typography.body,
    flex: 1,
  },

  // ─── Branches ────────────────────────────────────────────────────
  branchRow: {
    flexDirection: 'row',
    alignItems: 'center',
    borderRadius: radius.md,
    borderWidth: 1,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
    gap: spacing.sm,
  },
  branchIcon: {
    flexShrink: 0,
  },
  branchName: {
    ...typography.body,
    flex: 1,
  },
  currentBadge: {
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: radius.full,
  },
  currentBadgeText: {
    fontSize: 11,
    fontWeight: '600',
    color: '#fff',
  },

  // ─── Commits ─────────────────────────────────────────────────────
  commitRow: {
    borderRadius: radius.md,
    borderWidth: 1,
    padding: spacing.md,
  },
  commitTop: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginBottom: spacing.xs,
  },
  hashBadge: {
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: radius.xs,
  },
  commitHash: {
    fontSize: 12,
    fontFamily: 'monospace',
    fontWeight: '600',
  },
  commitTime: {
    ...typography.caption,
  },
  commitMessage: {
    ...typography.body,
    marginBottom: spacing.xs,
  },
  commitAuthor: {
    ...typography.caption,
  },

  // ─── Changes ─────────────────────────────────────────────────────
  changeRow: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    borderRadius: radius.md,
    borderWidth: 1,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    gap: spacing.sm,
  },
  statusBadge: {
    width: 22,
    paddingVertical: 2,
    borderRadius: radius.xs,
    alignItems: 'center',
    justifyContent: 'center',
    flexShrink: 0,
    marginTop: 1,
  },
  statusBadgeText: {
    fontSize: 11,
    fontWeight: '700',
    fontFamily: 'monospace',
  },
  changePath: {
    ...typography.body,
    flex: 1,
  },
  retryBtn: {
    marginTop: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.xs,
  },
  retryText: {
    ...typography.body,
    fontWeight: '600',
  },

  // ─── Worktree ────────────────────────────────────────────────────
  worktreeRow: {
    borderRadius: radius.md,
    borderWidth: 1,
    padding: spacing.md,
    gap: spacing.xs,
  },
  worktreeTop: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  worktreeBranch: {
    ...typography.bodyMedium,
    flex: 1,
  },
  changesBadge: {
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: radius.full,
  },
  changesBadgeText: {
    fontSize: 11,
    fontWeight: '600',
  },
  worktreePath: {
    ...typography.caption,
    paddingLeft: spacing.lg + spacing.sm,
  },
  worktreeWorkspaceRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
    paddingLeft: spacing.lg + spacing.sm,
    marginTop: spacing.xs,
  },
  worktreeWorkspaceText: {
    ...typography.caption,
  },
});
