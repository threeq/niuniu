import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import Ionicons from '@expo/vector-icons/Ionicons';
import {
  BottomSheetModal,
  BottomSheetBackdrop,
} from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import { BottomSheetScrollView } from '@gorhom/bottom-sheet';
import { api } from '../api/client';
import type {
  Issue,
  IssueChecklist,
  IssueComment,
  Workspace,
  Column,
} from '../api/types';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';
import { useRouter } from 'expo-router';
import { ConfirmDialog } from './ConfirmDialog';
import { CreateWorkspaceSheet } from './CreateWorkspaceSheet';
import { Badge } from './Badge';
import { SectionHeader } from './SectionHeader';
import { formatRelativeTime } from '../utils/formatTime';

// ─── Constants ───────────────────────────────────────────────────────────────

// Backend semantic: 0=low, 1=medium, 2=high, 3=critical (matches web SPA).
// Visual P1..P4 inverts: highest urgency (3) shows as P1.
const PRIORITY_LABELS: Record<number, string> = {
  3: 'P1 紧急',
  2: 'P2 高',
  1: 'P3 中',
  0: 'P4 低',
};

const PRIORITY_COLOR_KEYS: Record<number, 'p1' | 'p2' | 'p3' | 'p4'> = {
  3: 'p1',
  2: 'p2',
  1: 'p3',
  0: 'p4',
};

// Mirrors IssueCard.tsx wsStatusStyle / web SPA's kanban/IssueCard.tsx
// wsCardStyles. Hardcoded so workflow-state semantics don't shift with brand
// theme tweaks. bg=color-50, text=color-700, border=color-200.
type WorkspaceStatusKey =
  | 'created' | 'running' | 'needs_review' | 'attention' | 'completed';

const WS_STATUS_STYLE: Record<WorkspaceStatusKey, { text: string; bg: string; border: string; label: string }> = {
  created:      { text: '#52525B', bg: '#F4F4F5', border: '#E4E4E7', label: '已创建' },
  running:      { text: '#15803D', bg: '#F0FDF4', border: '#BBF7D0', label: '运行中' },
  needs_review: { text: '#C2410C', bg: '#FFF7ED', border: '#FED7AA', label: '待审核' },
  attention:    { text: '#B91C1C', bg: '#FEF2F2', border: '#FECACA', label: '需关注' },
  completed:    { text: '#1D4ED8', bg: '#EFF6FF', border: '#BFDBFE', label: '已完成' },
};

function resolveWsStatus(status: string): WorkspaceStatusKey {
  return (['created', 'running', 'needs_review', 'attention', 'completed'] as const).includes(
    status as WorkspaceStatusKey,
  )
    ? (status as WorkspaceStatusKey)
    : 'created';
}

const LIFECYCLE_LABELS: Record<string, string> = {
  created: '已创建',
  spec: '规格',
  'spec-review': '规格评审',
  plan: '计划',
  'plan-review': '计划评审',
  implement: '实现中',
  'implement-review': '代码评审',
  test: '测试',
  completed: '已完成',
};

// ─── Props ───────────────────────────────────────────────────────────────────

interface IssueDetailSheetProps {
  issueId: number;
  visible: boolean;
  onClose: () => void;
  // Parent (project detail screen) already has columns loaded — pass them in
  // to avoid a redundant /columns fetch and a second waterfall stage.
  columns?: Column[];
  // Required for the "create workspace" flow (lists project repos). Optional
  // because the sheet still works for read-only inspection without it.
  projectId?: number;
  // Issue data already in the parent's list state. When provided, the sheet
  // renders title/badges/description immediately while the full GET is still
  // in flight — turns the perceived 4s blank into a partial-then-fill UX.
  initialIssue?: Issue;
}

// ─── Sub-components ──────────────────────────────────────────────────────────

function ChecklistItemRow({
  item,
  onToggle,
}: {
  item: IssueChecklist;
  onToggle: (item: IssueChecklist) => void;
}) {
  const colors = useThemeColors();
  return (
    <Pressable style={styles.checkItem} onPress={() => onToggle(item)}>
      <View style={[
        styles.checkbox,
        { borderColor: colors.border.default },
        item.is_completed && { backgroundColor: colors.status.success, borderColor: colors.status.success },
      ]}>
        {item.is_completed && <Text style={styles.checkMark}>&#10003;</Text>}
      </View>
      <Text
        style={[
          styles.checkText,
          { color: colors.text.primary },
          item.is_completed && { textDecorationLine: 'line-through', color: colors.text.tertiary },
        ]}
      >
        {item.title}
      </Text>
    </Pressable>
  );
}

// ─── Main Component ──────────────────────────────────────────────────────────

export function IssueDetailSheet({
  issueId,
  visible,
  onClose,
  columns = [],
  projectId,
  initialIssue,
}: IssueDetailSheetProps) {
  const colors = useThemeColors();
  const router = useRouter();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const sheetRef = useRef<any>(null);
  const createWsSheetRef = useRef<BottomSheetModal>(null);

  // ── State ────────────────────────────────────────────────────────────────
  // Seed issue from initialIssue so the sheet body paints immediately;
  // the GET below will overwrite with the canonical detail when it lands.
  const [issue, setIssue] = useState<Issue | null>(initialIssue ?? null);
  const [checklists, setChecklists] = useState<IssueChecklist[]>([]);
  const [comments, setComments] = useState<IssueComment[]>([]);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  // Per-section loading flags. The sheet only renders the full-screen
  // spinner when there's no initialIssue AND the issue GET is still pending.
  const [loadingIssue, setLoadingIssue] = useState(true);
  const [loadingChecklists, setLoadingChecklists] = useState(true);
  const [loadingComments, setLoadingComments] = useState(true);
  const [loadingWorkspace, setLoadingWorkspace] = useState(true);
  const [newComment, setNewComment] = useState('');
  const [commenting, setCommenting] = useState(false);
  const [deleteDialogVisible, setDeleteDialogVisible] = useState(false);
  const [descExpanded, setDescExpanded] = useState(false);
  const [commentsExpanded, setCommentsExpanded] = useState(false);
  const [newChecklistItem, setNewChecklistItem] = useState('');

  // ── Snap points ──────────────────────────────────────────────────────────
  const snapPoints = useMemo(() => ['60%', '90%'], []);

  // ── Present / dismiss ────────────────────────────────────────────────────
  useEffect(() => {
    if (visible) {
      sheetRef.current?.present();
    } else {
      sheetRef.current?.dismiss();
    }
  }, [visible]);

  // ── Fetch data when issue changes or sheet opens ─────────────────────────
  useEffect(() => {
    if (!visible) return;

    // Reset sheet-local state. issue is seeded from initialIssue so the
    // body paints immediately when the user has tapped a list card; if
    // the sheet was opened without one (e.g. deep link, future code path),
    // the spinner will show until the issue GET below resolves.
    setDescExpanded(false);
    setCommentsExpanded(false);
    setNewComment('');
    setNewChecklistItem('');
    setDeleteDialogVisible(false);
    setIssue(initialIssue ?? null);
    setChecklists([]);
    setComments([]);
    setWorkspace(null);
    setLoadingIssue(true);
    setLoadingChecklists(true);
    setLoadingComments(true);
    setLoadingWorkspace(true);

    // Each request is fired independently and clears its own loading flag
    // as soon as it resolves, so the corresponding section unblocks without
    // waiting for the slowest sibling. Same total round-trip cost as a
    // bundled Promise.all but better perceived latency on a slow network.
    api
      .get<Issue>(`/issues/${issueId}`)
      .then((iss) => { if (iss) setIssue(iss); })
      .catch((err) => console.warn('Issue fetch error:', err))
      .finally(() => setLoadingIssue(false));

    api
      .get<IssueChecklist[]>(`/issues/${issueId}/checklists`)
      .then((checks) => setChecklists(checks ?? []))
      .catch((err) => console.warn('Checklists fetch error:', err))
      .finally(() => setLoadingChecklists(false));

    api
      .get<IssueComment[]>(`/issues/${issueId}/comments`)
      .then((cmts) => setComments(cmts ?? []))
      .catch((err) => console.warn('Comments fetch error:', err))
      .finally(() => setLoadingComments(false));

    // Workspace endpoint returns an array (`[]WorkspaceResponse`) or `null`
    // when the issue has none; take [0] for the single-card UI.
    api
      .get<Workspace[] | null>(`/issues/${issueId}/workspace`)
      .then((wsList) =>
        setWorkspace(Array.isArray(wsList) && wsList.length > 0 ? wsList[0] : null),
      )
      .catch(() => setWorkspace(null))
      .finally(() => setLoadingWorkspace(false));
  }, [visible, issueId, initialIssue]);

  // ── Callbacks ────────────────────────────────────────────────────────────
  const handleSheetDismiss = useCallback(() => {
    onClose();
  }, [onClose]);

  const refetchWorkspace = useCallback(async () => {
    try {
      const wsList = await api.get<Workspace[] | null>(`/issues/${issueId}/workspace`);
      setWorkspace(Array.isArray(wsList) && wsList.length > 0 ? wsList[0] : null);
    } catch {
      setWorkspace(null);
    }
  }, [issueId]);

  const renderBackdrop = useCallback(
    (props: BottomSheetBackdropProps) => (
      <BottomSheetBackdrop
        {...props}
        disappearsOnIndex={-1}
        appearsOnIndex={0}
        opacity={0.5}
      />
    ),
    [],
  );

  const toggleChecklist = async (item: IssueChecklist) => {
    try {
      await api.put(`/checklists/${item.id}`, {
        title: item.title,
        is_completed: !item.is_completed,
      });
      const checks = await api.get<IssueChecklist[]>(
        `/issues/${issueId}/checklists`,
      );
      setChecklists(checks ?? []);
    } catch (err) {
      console.warn('Toggle checklist failed:', err);
    }
  };

  const submitComment = async () => {
    if (!newComment.trim() || commenting) return;
    setCommenting(true);
    try {
      await api.post(`/issues/${issueId}/comments`, {
        content: newComment.trim(),
      });
      setNewComment('');
      const cmts = await api.get<IssueComment[]>(
        `/issues/${issueId}/comments`,
      );
      setComments(cmts ?? []);
    } catch (err) {
      console.warn('Comment failed:', err);
    } finally {
      setCommenting(false);
    }
  };

  const handleDelete = async () => {
    try {
      await api.delete(`/issues/${issueId}`);
      sheetRef.current?.dismiss();
    } catch (err) {
      console.warn('Delete issue failed:', err);
    }
  };

  const handleMoveToColumn = (columnId: number) => {
    Alert.alert('移至列', `即将把 Issue 移至列 ${columnId}`);
  };

  const handleAddChecklistItem = async () => {
    if (!newChecklistItem.trim()) return;
    try {
      const item = await api.post<IssueChecklist>(`/issues/${issueId}/checklists`, {
        title: newChecklistItem.trim(),
      });
      if (item) setChecklists((prev) => [...prev, item]);
      setNewChecklistItem('');
    } catch (err) {
      console.warn('Add checklist item failed:', err);
    }
  };

  // ── Derived values ───────────────────────────────────────────────────────
  const priorityKey = issue ? PRIORITY_COLOR_KEYS[issue.priority] : undefined;
  const priorityColor = priorityKey ? colors.priority[priorityKey] : undefined;
  const priorityBg = priorityKey ? colors.priority[`${priorityKey}Bg` as 'p1Bg' | 'p2Bg' | 'p3Bg' | 'p4Bg'] : undefined;
  const priorityLabel = issue ? PRIORITY_LABELS[issue.priority] ?? '' : '';
  const lifecycleLabel = issue
    ? LIFECYCLE_LABELS[issue.lifecycle_status] ?? issue.lifecycle_status
    : '';
  const checkDone = checklists.filter((c) => c.is_completed).length;
  const checkTotal = checklists.length;
  const checkPct = checkTotal > 0 ? (checkDone / checkTotal) * 100 : 0;

  // ── Render ───────────────────────────────────────────────────────────────
  return (
    <BottomSheetModal
        ref={sheetRef}
        index={0}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        onChange={(index) => { if (index === -1) handleSheetDismiss(); }}
        enablePanDownToClose
        backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: 20 }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        {!issue ? (
          // No initialIssue and GET still pending — full-screen spinner.
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color={colors.brand.accent} />
            <Text style={[styles.loadingText, { color: colors.text.secondary }]}>加载中...</Text>
          </View>
        ) : (
          <View style={styles.sheetContainer}>
            {/* Scrollable content */}
            <BottomSheetScrollView
              style={styles.scrollContent}
              contentContainerStyle={styles.scrollInner}
              showsVerticalScrollIndicator={false}
            >
              {/* Title + Status badges */}
              <Text style={[styles.title, { color: colors.text.primary }]}>{issue.title}</Text>
              <View style={styles.badgeRow}>
                <Badge
                  label={lifecycleLabel}
                  color={colors.brand.accent}
                  bgColor={colors.brand.accentLight}
                />
                {priorityColor && priorityBg && (
                  <Badge
                    label={priorityLabel}
                    color={priorityColor}
                    bgColor={priorityBg}
                  />
                )}
              </View>

              {/* Description */}
              {issue.description ? (
                <View style={styles.section}>
                  <SectionHeader title="描述" />
                  <Text
                    style={[styles.description, { color: colors.text.secondary }]}
                    numberOfLines={descExpanded ? undefined : 3}
                    onPress={() => setDescExpanded(!descExpanded)}
                  >
                    {issue.description}
                  </Text>
                  {issue.description.length > 100 && (
                    <TouchableOpacity onPress={() => setDescExpanded(!descExpanded)}>
                      <Text style={[styles.expandButton, { color: colors.brand.accent }]}>
                        {descExpanded ? '收起' : '展开全文'}
                      </Text>
                    </TouchableOpacity>
                  )}
                </View>
              ) : null}

              {/* Metadata grid — 80px label column */}
              <View style={styles.section}>
                <SectionHeader title="详情" />
                <View style={[styles.propertiesGrid, { backgroundColor: colors.bg.muted, borderRadius: radius.md }]}>
                  {[
                    { label: '负责人', value: issue.assignee },
                    { label: '开始日期', value: issue.start_date },
                    { label: '截止日期', value: issue.due_date },
                    { label: '预估', value: issue.estimate ? `${issue.estimate} ${issue.estimate_type ?? ''}` : undefined },
                  ].map(({ label, value }) =>
                    value ? (
                      <View key={label} style={[styles.propertyRow, { borderBottomColor: colors.border.subtle }]}>
                        <Text style={[styles.propertyLabel, { color: colors.text.tertiary }]}>{label}</Text>
                        <Text style={[styles.propertyValue, { color: colors.text.primary }]} numberOfLines={1}>{value}</Text>
                      </View>
                    ) : null
                  )}
                </View>
              </View>

              {/* Workspace binding info */}
              <View style={styles.section}>
                <SectionHeader title="工作空间" />
                {loadingWorkspace ? (
                  <View style={[styles.workspaceLoadingCard, { borderColor: colors.border.subtle }]}>
                    <ActivityIndicator size="small" color={colors.brand.accent} />
                  </View>
                ) : workspace ? (() => {
                  const wsKey = resolveWsStatus(workspace.status);
                  const wsStyle = WS_STATUS_STYLE[wsKey];
                  return (
                    <Pressable
                      style={[styles.workspaceCard, { backgroundColor: wsStyle.bg, borderColor: wsStyle.border }]}
                      onPress={() => {
                        // Dismiss sheet first so the back-stack lands on the
                        // project page when the user pops back from /workspace.
                        sheetRef.current?.dismiss();
                        router.push(`/workspace/${workspace.id}`);
                      }}
                    >
                      <View style={[styles.workspaceStatusDot, { backgroundColor: wsStyle.text }]} />
                      <View style={styles.workspaceInfo}>
                        <Text style={[styles.workspaceName, { color: colors.text.primary }]}>{workspace.name}</Text>
                        <View style={styles.workspaceMeta}>
                          <Text style={[styles.workspaceStatus, { color: wsStyle.text }]}>
                            {wsStyle.label}
                          </Text>
                          {workspace.changes_count != null && workspace.changes_count > 0 && (
                            <Text style={[styles.workspaceChanges, { color: colors.text.secondary }]}>
                              {workspace.changes_count} 变更
                            </Text>
                          )}
                        </View>
                      </View>
                      <Ionicons name="chevron-forward" size={16} color={colors.text.tertiary} />
                    </Pressable>
                  );
                })() : (
                  <View style={[styles.workspaceEmptyCard, { borderColor: colors.border.default }]}>
                    <Text style={[styles.workspaceEmptyText, { color: colors.text.tertiary }]}>暂无关联工作空间</Text>
                    {projectId != null && (
                      <TouchableOpacity
                        style={[styles.workspaceCreateBtn, { backgroundColor: colors.brand.accent }]}
                        onPress={() => createWsSheetRef.current?.present()}
                        activeOpacity={0.8}
                      >
                        <Ionicons name="add" size={16} color="#fff" />
                        <Text style={styles.workspaceCreateBtnText}>创建工作空间</Text>
                      </TouchableOpacity>
                    )}
                  </View>
                )}
              </View>

              {/* Checklist — always render so the user can add items even
                  before the GET resolves; show a spinner inline when loading
                  and no items yet so the section doesn't visibly "pop in" */}
              <View style={styles.section}>
                <View style={styles.checklistHeader}>
                  <SectionHeader title="检查清单" />
                  {!loadingChecklists && checkTotal > 0 && (
                    <Text style={[styles.checkProgress, { color: colors.text.secondary }]}>
                      {checkDone}/{checkTotal}
                    </Text>
                  )}
                </View>
                {loadingChecklists && checklists.length === 0 ? (
                  <View style={styles.sectionLoadingRow}>
                    <ActivityIndicator size="small" color={colors.brand.accent} />
                  </View>
                ) : (
                  <>
                    {checkTotal > 0 && (
                      <View style={[styles.progressBar, { backgroundColor: colors.border.subtle }]}>
                        <View style={[styles.progressFill, { width: `${checkPct}%`, backgroundColor: colors.status.success }]} />
                      </View>
                    )}
                    {checklists
                      .sort((a, b) => a.position - b.position)
                      .map((item) => (
                        <ChecklistItemRow
                          key={item.id}
                          item={item}
                          onToggle={toggleChecklist}
                        />
                      ))}
                    <View style={styles.addChecklistRow}>
                      <TextInput
                        style={[styles.checklistInput, { backgroundColor: colors.bg.base, color: colors.text.primary }]}
                        placeholder="添加检查项..."
                        placeholderTextColor={colors.text.tertiary}
                        value={newChecklistItem}
                        onChangeText={setNewChecklistItem}
                      />
                      <TouchableOpacity
                        style={[styles.addChecklistBtn, { backgroundColor: colors.brand.accent }, !newChecklistItem.trim() && styles.addChecklistBtnDisabled]}
                        disabled={!newChecklistItem.trim()}
                        onPress={handleAddChecklistItem}
                      >
                        <Ionicons name="add" size={20} color="#fff" />
                      </TouchableOpacity>
                    </View>
                  </>
                )}
              </View>

              {/* Comments — same per-section loading pattern */}
              <View style={styles.section}>
                {loadingComments && comments.length === 0 ? (
                  <>
                    <SectionHeader title="评论" />
                    <View style={styles.sectionLoadingRow}>
                      <ActivityIndicator size="small" color={colors.brand.accent} />
                    </View>
                  </>
                ) : comments.length > 0 ? (
                  <>
                    <TouchableOpacity
                      style={styles.commentsHeader}
                      onPress={() => setCommentsExpanded(!commentsExpanded)}
                    >
                      <SectionHeader title="评论" />
                      <Text style={[styles.commentsCount, { color: colors.text.tertiary }]}>
                        {commentsExpanded ? '▾' : '▸'} {comments.length} 条评论
                      </Text>
                    </TouchableOpacity>
                    {commentsExpanded && (
                      <>
                        {comments.map((c) => (
                          <View key={c.id} style={[styles.commentCard, { backgroundColor: colors.bg.muted }]}>
                            <Text style={[styles.commentAuthor, { color: colors.text.primary }]}>{c.author}</Text>
                            <Text style={[styles.commentContent, { color: colors.text.secondary }]}>{c.content}</Text>
                            <Text style={[styles.commentTime, { color: colors.text.tertiary }]}>{formatRelativeTime(c.created_at)}</Text>
                          </View>
                        ))}
                      </>
                    )}
                  </>
                ) : null}
              </View>

              {/* Extra bottom padding for comment input */}
              <View style={styles.bottomSpacer} />
            </BottomSheetScrollView>

            {/* Fixed bottom action bar */}
            <View style={[styles.actionBar, { borderTopColor: colors.border.default, backgroundColor: colors.bg.surface }]}>
              {/* Comment input row */}
              <View style={styles.commentInputRow}>
                <TextInput
                  style={[styles.commentInput, { backgroundColor: colors.bg.muted, color: colors.text.primary, borderColor: colors.border.default }]}
                  placeholder="添加评论..."
                  placeholderTextColor={colors.text.tertiary}
                  value={newComment}
                  onChangeText={setNewComment}
                  multiline
                />
                <Pressable
                  style={[
                    styles.commentSendBtn,
                    { backgroundColor: colors.brand.accent },
                    (!newComment.trim() || commenting) && { opacity: 0.4 },
                  ]}
                  onPress={submitComment}
                  disabled={!newComment.trim() || commenting}
                >
                  <Text style={[styles.commentSendText, { color: '#fff' }]}>发送</Text>
                </Pressable>
              </View>

              {/* Action row: primary "Move to..." button + overflow menu */}
              <View style={styles.actionButtons}>
                <Pressable
                  style={[styles.actionBtnPrimary, { backgroundColor: colors.brand.accent }]}
                  onPress={() => {
                    if (columns.length > 0) {
                      Alert.alert('移至列', '选择目标列', [
                        ...columns.map((c) => ({
                          text: c.name,
                          onPress: () => handleMoveToColumn(c.id),
                        })),
                        { text: '取消', style: 'cancel' },
                      ]);
                    }
                  }}
                >
                  <Text style={[styles.actionBtnTextPrimary, { color: '#fff' }]}>移至...</Text>
                </Pressable>
                <Pressable
                  style={[styles.overflowBtn, { backgroundColor: colors.bg.muted }]}
                  onPress={() => setDeleteDialogVisible(true)}
                >
                  <Ionicons name="ellipsis-horizontal" size={18} color={colors.text.secondary} />
                </Pressable>
              </View>
            </View>
          </View>
        )}

        {/* Delete confirmation dialog */}
        <ConfirmDialog
          visible={deleteDialogVisible}
          title="删除 Issue"
          message="确定要删除这个 Issue 吗？此操作不可撤销。"
          confirmLabel="删除"
          onConfirm={handleDelete}
          onCancel={() => setDeleteDialogVisible(false)}
          destructive
        />

        {/* Create workspace sheet — only mounted when projectId is known */}
        {projectId != null && issue && (
          <CreateWorkspaceSheet
            ref={createWsSheetRef}
            issueId={issueId}
            projectId={projectId}
            defaultName={issue.title}
            onCreated={refetchWorkspace}
          />
        )}
      </BottomSheetModal>
  );
}

// ─── Styles ──────────────────────────────────────────────────────────────────

const styles = StyleSheet.create({
  // Sheet container
  sheetContainer: {
    flex: 1,
  },
  scrollContent: {
    flex: 1,
  },
  scrollInner: {
    paddingHorizontal: spacing.xl,
    paddingTop: spacing.sm,
  },
  bottomSpacer: {
    height: 160,
  },

  // Loading
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingVertical: spacing['3xl'],
  },
  loadingText: {
    ...typography.body,
    marginTop: spacing.md,
  },

  // Title & badges
  title: {
    ...typography.sectionHead,
    marginBottom: spacing.md,
  },
  badgeRow: {
    flexDirection: 'row',
    gap: spacing.sm,
    marginBottom: spacing.xl,
    flexWrap: 'wrap',
  },

  // Section
  section: {
    marginBottom: spacing.xl,
  },

  // Workspace card
  workspaceCard: {
    flexDirection: 'row',
    alignItems: 'center',
    borderRadius: radius.md,
    borderWidth: 1,
    padding: spacing.lg,
    gap: spacing.md,
  },
  workspaceStatusDot: {
    width: 8,
    height: 8,
    borderRadius: 4,
  },
  workspaceInfo: {
    flex: 1,
  },
  workspaceName: {
    ...typography.bodyMedium,
  },
  workspaceMeta: {
    flexDirection: 'row',
    gap: spacing.md,
    marginTop: spacing.xs,
  },
  workspaceStatus: {
    ...typography.caption,
  },
  workspaceChanges: {
    ...typography.caption,
  },
  workspaceEmptyCard: {
    borderWidth: 1,
    borderStyle: 'dashed',
    borderRadius: radius.md,
    padding: spacing.lg,
    alignItems: 'center',
  },
  workspaceEmptyText: {
    ...typography.body,
    marginBottom: spacing.md,
  },
  workspaceCreateBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    borderRadius: radius.md,
  },
  workspaceCreateBtnText: {
    ...typography.label,
    color: '#fff',
    fontWeight: '600',
  },
  workspaceLoadingCard: {
    borderWidth: 1,
    borderRadius: radius.md,
    paddingVertical: spacing.lg,
    alignItems: 'center',
  },
  sectionLoadingRow: {
    paddingVertical: spacing.md,
    alignItems: 'center',
  },

  // Description
  description: {
    ...typography.body,
    lineHeight: 22,
  },
  expandButton: {
    fontSize: 13,
    marginTop: 4,
  },

  // Properties grid (80px label column)
  propertiesGrid: {
    overflow: 'hidden',
  },
  propertyRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: spacing.sm,
    paddingHorizontal: spacing.md,
    borderBottomWidth: 1,
  },
  propertyLabel: {
    ...typography.caption,
    width: 80,
  },
  propertyValue: {
    ...typography.label,
    flex: 1,
  },

  // Checklist
  checklistHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  checkProgress: {
    ...typography.caption,
    fontVariant: ['tabular-nums'],
  },
  progressBar: {
    height: 2,
    borderRadius: 1,
    marginBottom: spacing.md,
    overflow: 'hidden',
  },
  progressFill: {
    height: '100%',
    borderRadius: 1,
  },
  checkItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: spacing.sm,
  },
  checkbox: {
    width: 18,
    height: 18,
    borderRadius: 4,
    borderWidth: 1.5,
    marginRight: spacing.md,
    alignItems: 'center',
    justifyContent: 'center',
  },
  checkMark: {
    color: '#fff',
    fontSize: 11,
    fontWeight: '700',
  },
  checkText: {
    ...typography.body,
    flex: 1,
  },
  addChecklistRow: {
    flexDirection: 'row',
    gap: spacing.sm,
    marginTop: spacing.sm,
  },
  checklistInput: {
    flex: 1,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    fontSize: 14,
  },
  addChecklistBtn: {
    width: 36,
    height: 36,
    borderRadius: radius.sm,
    justifyContent: 'center',
    alignItems: 'center',
  },
  addChecklistBtnDisabled: {
    opacity: 0.4,
  },

  // Comments
  commentsHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: spacing.sm,
  },
  commentsCount: {
    fontSize: 13,
  },
  commentCard: {
    borderRadius: radius.md,
    padding: spacing.md,
    marginBottom: spacing.sm,
  },
  commentAuthor: {
    fontSize: 13,
    fontWeight: '600',
    marginBottom: 4,
  },
  commentContent: {
    fontSize: 14,
    lineHeight: 20,
    marginBottom: 4,
  },
  commentTime: {
    fontSize: 11,
  },

  // Action bar (fixed bottom)
  actionBar: {
    borderTopWidth: 1,
    paddingHorizontal: spacing.xl,
    paddingTop: spacing.md,
    paddingBottom: spacing.xl,
  },
  commentInputRow: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    gap: spacing.sm,
    marginBottom: spacing.md,
  },
  commentInput: {
    flex: 1,
    borderRadius: radius.md,
    padding: spacing.md,
    maxHeight: 72,
    ...typography.body,
    borderWidth: 1,
  },
  commentSendBtn: {
    borderRadius: radius.md,
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
  },
  commentSendText: {
    ...typography.bodyMedium,
  },
  actionButtons: {
    flexDirection: 'row',
    gap: spacing.sm,
    alignItems: 'center',
  },
  actionBtnPrimary: {
    flex: 1,
    borderRadius: radius.md,
    paddingVertical: spacing.md,
    paddingHorizontal: spacing.lg,
    alignItems: 'center',
  },
  actionBtnTextPrimary: {
    ...typography.bodyMedium,
    fontWeight: '600',
  },
  overflowBtn: {
    width: 40,
    height: 40,
    borderRadius: radius.sm,
    justifyContent: 'center',
    alignItems: 'center',
  },
});
