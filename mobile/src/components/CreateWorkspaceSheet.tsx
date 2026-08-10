import React, {
  forwardRef,
  useCallback,
  useMemo,
  useState,
} from 'react';
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
import {
  BottomSheetModal,
  BottomSheetView,
  BottomSheetBackdrop,
  BottomSheetScrollView,
} from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import Ionicons from '@expo/vector-icons/Ionicons';
import { api } from '../api/client';
import type { BranchInfo, ProjectRepositoryBinding, Repository } from '../api/types';
import { useThemeColors } from '../theme/useTheme';
import { radius, spacing, typography } from '../theme/tokens';

// ─── Props ───────────────────────────────────────────────────────────────────

interface CreateWorkspaceSheetProps {
  issueId: number;
  projectId: number;
  defaultName?: string;
  onCreated: () => void;
}

interface RepoSelection {
  selected: boolean;
  branch: string;
}

// ─── Component ───────────────────────────────────────────────────────────────

export const CreateWorkspaceSheet = forwardRef<BottomSheetModal, CreateWorkspaceSheetProps>(
  function CreateWorkspaceSheet({ issueId, projectId, defaultName, onCreated }, ref) {
    const colors = useThemeColors();

    // ── State ────────────────────────────────────────────────────────────────
    const [name, setName] = useState(defaultName ?? '');
    // Full list of user-accessible repositories — the basis for the picker.
    // Project-bound ones are merely pre-checked; user can include any other.
    // Mirrors web SPA's create-issue-dialog.tsx: allRepositories +
    // projectDetail.repositories pre-selection logic.
    const [allRepos, setAllRepos] = useState<Repository[]>([]);
    const [bindings, setBindings] = useState<ProjectRepositoryBinding[]>([]);
    const [branchesByRepo, setBranchesByRepo] = useState<Record<number, string[]>>({});
    const [selectionByRepo, setSelectionByRepo] = useState<Record<number, RepoSelection>>({});
    const [loading, setLoading] = useState(false);
    const [submitting, setSubmitting] = useState(false);

    const snapPoints = useMemo(() => ['80%'], []);

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

    // ── Load repos + branches on open ────────────────────────────────────────
    const loadRepos = useCallback(async () => {
      setLoading(true);
      try {
        // Pull project bindings AND the full user-accessible repo list in
        // parallel. The project bindings are used purely for pre-selection
        // and "project default branch" resolution; the picker shows every
        // accessible repo so the user can include unbound ones too.
        const [bindingList, repoList] = await Promise.all([
          api
            .get<ProjectRepositoryBinding[]>(`/projects/${projectId}/repositories`)
            .catch(() => [] as ProjectRepositoryBinding[]),
          api.get<Repository[]>(`/repositories`).catch(() => [] as Repository[]),
        ]);
        const safeBindings = bindingList ?? [];
        const safeRepos = repoList ?? [];
        setBindings(safeBindings);
        setAllRepos(safeRepos);

        // Pre-check repos that are bound to the project; default-branch
        // resolution prefers the project-level override when present.
        const bindingByRepoId: Record<number, ProjectRepositoryBinding> = {};
        for (const b of safeBindings) {
          bindingByRepoId[b.repository_id] = b;
        }
        const initSelection: Record<number, RepoSelection> = {};
        for (const r of safeRepos) {
          const b = bindingByRepoId[r.id];
          initSelection[r.id] = {
            selected: b != null,
            branch:
              b?.project_default_branch ||
              b?.repo_default_branch ||
              r.default_branch ||
              'main',
          };
        }
        setSelectionByRepo(initSelection);

        // Fetch branches for every repo in parallel — best-effort, individual
        // failures fall back to the default-branch picker only.
        const branchResults = await Promise.all(
          safeRepos.map(async (r) => {
            try {
              const info = await api.get<BranchInfo>(`/repositories/${r.id}/branches`);
              return { id: r.id, branches: info?.branches ?? [] };
            } catch {
              return { id: r.id, branches: [] as string[] };
            }
          }),
        );
        const branchMap: Record<number, string[]> = {};
        for (const { id, branches } of branchResults) {
          branchMap[id] = branches;
        }
        setBranchesByRepo(branchMap);
      } catch (err) {
        console.warn('CreateWorkspaceSheet load error:', err);
        Alert.alert('加载仓库失败', '请稍后重试');
      } finally {
        setLoading(false);
      }
    }, [projectId]);

    const handleSheetChange = useCallback(
      (index: number) => {
        if (index === 0) {
          // Reset name to issue title on every open
          setName(defaultName ?? '');
          setAllRepos([]);
          setBindings([]);
          setBranchesByRepo({});
          setSelectionByRepo({});
          loadRepos();
        }
      },
      [loadRepos, defaultName],
    );

    // ── Repo row interactions ────────────────────────────────────────────────
    const toggleRepo = (repoId: number) => {
      setSelectionByRepo((prev) => ({
        ...prev,
        [repoId]: { ...prev[repoId], selected: !prev[repoId]?.selected },
      }));
    };

    const pickBranch = (repoId: number) => {
      const branches = branchesByRepo[repoId] ?? [];
      if (branches.length === 0) {
        Alert.alert('暂无可选分支', '该仓库没有可选分支，将使用默认分支');
        return;
      }
      Alert.alert(
        '选择基线分支',
        '',
        [
          ...branches.map((b) => ({
            text: b,
            onPress: () =>
              setSelectionByRepo((prev) => ({
                ...prev,
                [repoId]: { ...prev[repoId], branch: b },
              })),
          })),
          { text: '取消', style: 'cancel' as const },
        ],
      );
    };

    // ── Submit ───────────────────────────────────────────────────────────────
    const handleSubmit = async () => {
      if (!name.trim()) {
        Alert.alert('请填写名称', '工作空间名称为必填项');
        return;
      }
      // Zero repos is allowed — backend WorkspaceService.Create skips the
      // worktree-add loop when input.Repos is empty, leaving an empty
      // workspace shell the user can attach repos to later.
      const repos = allRepos
        .filter((r) => selectionByRepo[r.id]?.selected)
        .map((r) => {
          const sel = selectionByRepo[r.id];
          return {
            repo_id: r.id,
            branch: sel.branch || r.default_branch || 'main',
          };
        });
      if (submitting) return;

      setSubmitting(true);
      try {
        await api.post(`/issues/${issueId}/workspace`, {
          name: name.trim(),
          repos,
        });
        (ref as React.RefObject<BottomSheetModal>)?.current?.dismiss();
        onCreated();
      } catch (err) {
        console.warn('CreateWorkspaceSheet submit error:', err);
        Alert.alert('创建失败', '无法创建工作空间，请稍后重试');
      } finally {
        setSubmitting(false);
      }
    };

    // ── Render ───────────────────────────────────────────────────────────────
    return (
      <BottomSheetModal
        ref={ref}
        index={0}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        onChange={handleSheetChange}
        enablePanDownToClose
        backgroundStyle={{ backgroundColor: colors.bg.surface, borderRadius: 20 }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView style={styles.container}>
          <Text style={[styles.header, { color: colors.text.primary }]}>
            创建工作空间
          </Text>

          {/* 名称 */}
          <View style={styles.fieldGroup}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>
              名称 <Text style={{ color: colors.status.error }}>*</Text>
            </Text>
            <TextInput
              style={[
                styles.input,
                {
                  backgroundColor: colors.bg.muted,
                  color: colors.text.primary,
                  borderColor: colors.border.default,
                },
              ]}
              placeholder="工作空间名称..."
              placeholderTextColor={colors.text.tertiary}
              value={name}
              onChangeText={setName}
              maxLength={120}
            />
          </View>

          {/* 仓库列表 */}
          <View style={[styles.fieldGroup, styles.flex1]}>
            <Text style={[styles.label, { color: colors.text.secondary }]}>仓库</Text>
            {loading ? (
              <View style={styles.loadingBox}>
                <ActivityIndicator size="small" color={colors.brand.accent} />
              </View>
            ) : allRepos.length === 0 ? (
              <View style={[styles.emptyBox, { borderColor: colors.border.default }]}>
                <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
                  暂无可用仓库，将创建空工作空间
                </Text>
              </View>
            ) : (
              <BottomSheetScrollView
                style={styles.repoScroll}
                contentContainerStyle={styles.repoScrollContent}
                showsVerticalScrollIndicator={false}
              >
                {allRepos.map((r) => {
                  const sel = selectionByRepo[r.id];
                  const checked = sel?.selected ?? false;
                  const branch = sel?.branch ?? '';
                  const bound = bindings.some((b) => b.repository_id === r.id);
                  return (
                    <View
                      key={r.id}
                      style={[styles.repoRow, { borderColor: colors.border.subtle }]}
                    >
                      <Pressable
                        style={styles.repoMain}
                        onPress={() => toggleRepo(r.id)}
                      >
                        <View
                          style={[
                            styles.checkbox,
                            { borderColor: colors.border.default },
                            checked && {
                              backgroundColor: colors.brand.accent,
                              borderColor: colors.brand.accent,
                            },
                          ]}
                        >
                          {checked && (
                            <Ionicons name="checkmark" size={14} color="#fff" />
                          )}
                        </View>
                        <View style={styles.repoTextWrap}>
                          <Text
                            style={[styles.repoName, { color: colors.text.primary }]}
                            numberOfLines={1}
                          >
                            {r.name}
                          </Text>
                          {bound && (
                            <Text
                              style={[styles.repoBoundTag, { color: colors.brand.accent }]}
                            >
                              项目仓库
                            </Text>
                          )}
                        </View>
                      </Pressable>
                      <Pressable
                        style={[
                          styles.branchPill,
                          {
                            backgroundColor: colors.bg.muted,
                            borderColor: colors.border.default,
                            opacity: checked ? 1 : 0.4,
                          },
                        ]}
                        disabled={!checked}
                        onPress={() => pickBranch(r.id)}
                      >
                        <Ionicons
                          name="git-branch-outline"
                          size={12}
                          color={colors.text.secondary}
                        />
                        <Text
                          style={[styles.branchText, { color: colors.text.secondary }]}
                          numberOfLines={1}
                        >
                          {branch}
                        </Text>
                      </Pressable>
                    </View>
                  );
                })}
              </BottomSheetScrollView>
            )}
          </View>

          {/* Submit — only blocks on missing name; empty repos is allowed */}
          <TouchableOpacity
            style={[
              styles.submitBtn,
              { backgroundColor: colors.brand.accent },
              (!name.trim() || submitting || loading) && { opacity: 0.5 },
            ]}
            onPress={handleSubmit}
            disabled={!name.trim() || submitting || loading}
            activeOpacity={0.8}
          >
            {submitting ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.submitText}>创建工作空间</Text>
            )}
          </TouchableOpacity>
        </BottomSheetView>
      </BottomSheetModal>
    );
  },
);

// ─── Styles ──────────────────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: {
    flex: 1,
    paddingHorizontal: spacing.xl,
    paddingBottom: spacing.xl,
  },
  flex1: {
    flex: 1,
  },
  header: {
    ...typography.sectionHead,
    marginBottom: spacing.xl,
    marginTop: spacing.sm,
  },
  fieldGroup: {
    marginBottom: spacing.lg,
  },
  label: {
    ...typography.label,
    marginBottom: spacing.sm,
  },
  input: {
    borderWidth: 1,
    borderRadius: radius.md,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    ...typography.body,
  },
  loadingBox: {
    paddingVertical: spacing.xl,
    alignItems: 'center',
  },
  emptyBox: {
    borderWidth: 1,
    borderStyle: 'dashed',
    borderRadius: radius.md,
    padding: spacing.lg,
    alignItems: 'center',
  },
  emptyText: {
    ...typography.body,
  },
  repoScroll: {
    flex: 1,
  },
  repoScrollContent: {
    paddingBottom: spacing.sm,
  },
  repoRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingVertical: spacing.md,
    borderBottomWidth: 1,
    gap: spacing.md,
  },
  repoBoundTag: {
    fontSize: 10,
    fontWeight: '500',
    marginTop: 2,
  },
  repoMain: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
    gap: spacing.md,
  },
  checkbox: {
    width: 20,
    height: 20,
    borderRadius: 4,
    borderWidth: 1.5,
    alignItems: 'center',
    justifyContent: 'center',
  },
  repoTextWrap: {
    flex: 1,
  },
  repoName: {
    ...typography.bodyMedium,
  },
  branchPill: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    paddingHorizontal: spacing.sm,
    paddingVertical: 4,
    borderRadius: radius.full,
    borderWidth: 1,
    maxWidth: 140,
  },
  branchText: {
    ...typography.caption,
  },
  submitBtn: {
    marginTop: spacing.md,
    borderRadius: radius.md,
    paddingVertical: spacing.md,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 48,
  },
  submitText: {
    ...typography.bodyMedium,
    color: '#fff',
    fontWeight: '600',
  },
});
