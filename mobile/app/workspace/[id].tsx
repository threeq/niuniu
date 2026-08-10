import React, { useEffect, useState, useCallback, useRef, useMemo } from 'react';
import {
  View,
  Text,
  StyleSheet,
  FlatList,
  TextInput,
  Pressable,
  KeyboardAvoidingView,
  Platform,
  ActivityIndicator,
  Alert,
  TouchableOpacity,
  ScrollView,
  Image,
  Modal,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useLocalSearchParams, useRouter, Stack } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import {
  BottomSheetModal,
  BottomSheetView,
  BottomSheetFlatList,
  BottomSheetBackdrop,
} from '@gorhom/bottom-sheet';
import type { BottomSheetBackdropProps } from '@gorhom/bottom-sheet';
import * as ImagePicker from 'expo-image-picker';
import Markdown from 'react-native-markdown-display';
import { useThemeColors } from '../../src/theme/useTheme';
import { typography, spacing, radius } from '../../src/theme/tokens';
import { api, apiUpload } from '../../src/api/client';
import { useServerStore } from '../../src/stores/serverStore';
import { useAuthStore } from '../../src/stores/authStore';
import { useTransportStore } from '../../src/stores/transportStore';
import type {
  Workspace,
  WorkspaceTask,
  QueueItem,
  ChatAttachment,
  AttachmentUploadResponse,
  AgentMessagesResponse,
} from '../../src/api/types';
import { useWorkspaceChatStore, type ChatMessage } from '../../src/stores/workspaceChatStore';
import { useWorkspaceDetailStore } from '../../src/stores/workspaceDetailStore';
import { useWorkspaceSSE } from '../../src/hooks/useWorkspaceSSE';
import { StatusBadge } from '../../src/components/StatusBadge';
import { ConfirmDialog } from '../../src/components/ConfirmDialog';
import { IssueDetailSheet } from '../../src/components/IssueDetailSheet';

type AgentStatus = 'running' | 'completed' | 'failed' | 'idle';

const AGENT_STATUSES: readonly AgentStatus[] = ['running', 'completed', 'failed', 'idle'];

const EMPTY_MESSAGES: ChatMessage[] = [];

function agentStatusLabel(status: string): AgentStatus {
  return (AGENT_STATUSES as readonly string[]).includes(status)
    ? (status as AgentStatus)
    : 'idle';
}

type ConfirmKind = 'clear' | 'complete' | null;

// HH:mm absolute time, mirrors SPA chat-message.tsx formatTime. For
// messages from previous days we prepend MM-DD so the page stays useful
// when scrolling back into history (SPA doesn't bother because its chat
// panel rarely shows >24h of context, but mobile chat history is loaded
// in full).
function formatChatTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const now = new Date();
  const sameDay =
    d.getFullYear() === now.getFullYear() &&
    d.getMonth() === now.getMonth() &&
    d.getDate() === now.getDate();
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  if (sameDay) return `${hh}:${mm}`;
  const M = String(d.getMonth() + 1).padStart(2, '0');
  const D = String(d.getDate()).padStart(2, '0');
  return `${M}-${D} ${hh}:${mm}`;
}

// Server stores attachments as a JSON string on agent_messages.attachments;
// parse defensively because it's stored verbatim.
function safeParseAttachments(raw?: string): ChatAttachment[] | undefined {
  if (!raw) return undefined;
  try {
    const arr = JSON.parse(raw);
    if (Array.isArray(arr) && arr.length > 0) return arr as ChatAttachment[];
  } catch {
    /* ignore */
  }
  return undefined;
}

// SPA-equivalent helpers for attachment rendering. Server appends a
// "\n\n[附件: path1, path2]" suffix to user content when the message has
// attachments (see agentproxy.go SendMessage finalContent), so we strip
// that on render to keep the visible text clean and show real chips below.
function stripAttachmentSuffix(content: string, atts?: ChatAttachment[]): string {
  if (!atts || atts.length === 0) return content;
  const suffix = `\n\n[附件: ${atts.map((a) => a.path).join(', ')}]`;
  return content.endsWith(suffix) ? content.slice(0, -suffix.length) : content;
}

const IMAGE_EXT = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'bmp', 'avif']);

function isImageAttachment(a: ChatAttachment): boolean {
  if (a.mimeType?.startsWith('image/')) return true;
  const ext = a.name.split('.').pop()?.toLowerCase() ?? '';
  return IMAGE_EXT.has(ext);
}

function formatBytes(bytes: number | undefined): string {
  if (bytes == null) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

// Server file-content URL with token in query param (server's MCPTokenAuth
// path is separate; the regular auth middleware accepts ?token=). Returns
// null when on a relay transport — we don't have a public URL for the
// <Image> source to fetch through the encrypted tunnel.
function getFileContentUrl(workspaceId: string, path: string): string | null {
  const activeDesktopId = useTransportStore.getState().activeDesktopId;
  if (activeDesktopId) return null;
  const baseUrl = useServerStore.getState().getBaseUrl();
  if (!baseUrl) return null;
  const token = useAuthStore.getState().accessToken;
  const params = new URLSearchParams({ path, mode: 'raw' });
  if (token) params.set('token', token);
  return `${baseUrl}/api/workspaces/${workspaceId}/file-content?${params.toString()}`;
}

// Group consecutive tool_use messages so multiple back-to-back tool calls
// collapse into one expandable card (mirrors SPA group-timeline-events.ts).
// A non-tool message (text / thinking / etc.) breaks the current group so
// assistant text always stays visible — never hidden inside a collapsed group.
type TimelineItem = ChatMessage | ChatMessage[];

function groupMessages(msgs: ChatMessage[]): TimelineItem[] {
  const result: TimelineItem[] = [];
  let group: ChatMessage[] = [];
  for (const m of msgs) {
    if (m.type === 'tool_use') {
      group.push(m);
    } else {
      if (group.length > 0) {
        result.push(group.length === 1 ? group[0] : group);
        group = [];
      }
      result.push(m);
    }
  }
  if (group.length > 0) {
    result.push(group.length === 1 ? group[0] : group);
  }
  return result;
}

export default function WorkspaceDetailScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const colors = useThemeColors();
  const router = useRouter();
  const insets = useSafeAreaInsets();

  // Cache-first: if we've loaded this workspace before in this session,
  // render it immediately. The background refresh below updates fields
  // (status, total_cost, etc.) when the GET resolves. Cold first visit
  // still spins, but only on a single GET (~100–300ms), not on the old
  // GET-then-GET-history chain (~1–3s).
  const cachedWorkspace = useWorkspaceDetailStore((s) =>
    id ? s.workspaces[id] : undefined,
  );
  const [workspace, setWorkspace] = useState<Workspace | null>(
    cachedWorkspace ?? null,
  );
  const [loading, setLoading] = useState(!cachedWorkspace);
  const [input, setInput] = useState('');
  const [sending, setSending] = useState(false);
  const [showMenu, setShowMenu] = useState(false);
  const [tasks, setTasks] = useState<WorkspaceTask[] | null>(null);
  const [queue, setQueue] = useState<QueueItem[] | null>(null);
  const [issueSheetOpen, setIssueSheetOpen] = useState(false);
  const [confirm, setConfirm] = useState<ConfirmKind>(null);
  const [attachments, setAttachments] = useState<ChatAttachment[]>([]);
  const [uploadingCount, setUploadingCount] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [hasMoreHistory, setHasMoreHistory] = useState(true);
  const [lightboxUri, setLightboxUri] = useState<string | null>(null);
  const tasksSheetRef = useRef<BottomSheetModal>(null);
  const queueSheetRef = useRef<BottomSheetModal>(null);

  const messages = useWorkspaceChatStore((s) => (id ? s.messages[id] ?? EMPTY_MESSAGES : EMPTY_MESSAGES));
  const groupedReversed = useMemo(
    () => [...groupMessages(messages)].reverse(),
    [messages],
  );
  const agentStatus = useWorkspaceChatStore((s) => (id ? s.agentStatus[id] ?? 'idle' : 'idle'));
  const typing = useWorkspaceChatStore((s) => (id ? s.typing[id] ?? false : false));

  // useWorkspaceSSE owns history loading now: its first poll is `?limit=20`
  // on a cold cache or `?after=<cursor>` on a revisit (cursor lives in the
  // chat store). The screen no longer fetches /messages itself.
  const { historyLoaded } = useWorkspaceSSE(id);
  // Show an in-area chat loading indicator only when we have nothing to
  // show AND the first poll hasn't settled. On revisits, `messages` is
  // non-empty from the chat store cache, so this stays false and the
  // chat renders instantly.
  const chatLoading = !historyLoaded && messages.length === 0;

  const loadWorkspace = useCallback(async () => {
    if (!id) return;
    try {
      const ws = await api.get<Workspace>(`/workspaces/${id}`);
      setWorkspace(ws);
      useWorkspaceDetailStore.getState().setWorkspace(id, ws);
    } catch (err: any) {
      // Don't alert when we're refreshing a cached shell — the user is
      // looking at the previous (still-valid) data and a transient
      // network blip shouldn't pop a modal.
      if (!cachedWorkspace) {
        Alert.alert('加载失败', err?.message ?? '无法加载工作空间');
      }
    } finally {
      setLoading(false);
    }
  }, [id, cachedWorkspace]);

  useEffect(() => {
    loadWorkspace();
  }, [loadWorkspace]);

  // Reset pagination state when workspace id changes — a workspace with
  // fewer than 20 messages would otherwise inherit hasMoreHistory=false
  // from a previous mount and never page in older messages even if the
  // new workspace has plenty of history.
  useEffect(() => {
    setHasMoreHistory(true);
    setLoadingMore(false);
    setLightboxUri(null);
  }, [id]);

  const handleSend = async () => {
    const trimmed = input.trim();
    if (!id || sending) return;
    if (!trimmed && attachments.length === 0) return;

    // Strip localUri before sending — it's a mobile-only field for preview.
    const serverAttachments = attachments.map(({ localUri: _u, ...rest }) => rest);
    const payload = {
      content: trimmed,
      attachments: serverAttachments.length > 0 ? serverAttachments : undefined,
    };
    // Match server agentproxy.go SendMessage finalContent format exactly
    // (paths joined, not names) so the SSE poll's content-based dedup
    // skips this optimistic message instead of duplicating it.
    const localContent =
      attachments.length > 0
        ? `${trimmed}${trimmed ? '\n\n' : ''}[附件: ${attachments.map((a) => a.path).join(', ')}]`
        : trimmed;
    const localAttachments = attachments.length > 0 ? attachments : undefined;

    setInput('');
    setAttachments([]);
    setSending(true);

    useWorkspaceChatStore.getState().addUserMessage(id, localContent, localAttachments);
    useWorkspaceChatStore.getState().setAgentStatus(id, 'running');

    try {
      // POST /workspaces/:id/messages is the chat endpoint (returns 202
      // {status, queueId?}). The PTY-based /agent/send endpoint expects
      // {input} not {content} and is for the terminal flow.
      await api.post(`/workspaces/${id}/messages`, payload);
    } catch (err: any) {
      Alert.alert('发送失败', err?.message ?? '无法发送消息');
    } finally {
      setSending(false);
    }
  };

  // Load older history when the inverted FlatList scrolls to its end (top
  // of visible older-messages direction). Uses ?before=<oldestServerId>
  // paging. The first message in the store array is the oldest because the
  // store keeps messages in ASC chronological order.
  const loadMoreHistory = useCallback(async () => {
    if (!id || loadingMore || !hasMoreHistory) return;
    if (messages.length === 0) return;
    // Find the oldest message that has a server id. Optimistic inserts
    // (handleSend before the next poll reconciles) have no serverId, so
    // skip past them to the first reconciled bubble. If everything in
    // store is optimistic there's nothing for the server to page from.
    const oldestWithServerId = messages.find((m) => m.serverId);
    if (!oldestWithServerId?.serverId) return;

    setLoadingMore(true);
    try {
      const res = await api.get<AgentMessagesResponse>(
        `/workspaces/${id}/messages?before=${encodeURIComponent(oldestWithServerId.serverId)}&limit=20`,
      );
      const older = res?.messages ?? [];
      if (older.length > 0) {
        const converted: ChatMessage[] = [];
        for (const m of older) {
          const timestamp = new Date(m.createdAt).toISOString();
          if (m.eventType === 'tool_use') {
            converted.push({
              id: m.id,
              serverId: m.id,
              role: 'assistant',
              type: 'tool_use',
              content: '',
              timestamp,
              toolName: m.toolName ?? 'tool',
              toolInput: m.toolInput ?? '',
              messageId: m.messageId,
            });
          } else if (m.eventType === 'tool_result') {
            // Tool results update an existing tool_use's status; for paged
            // history the matching tool_use is already in store, mutate it.
            useWorkspaceChatStore.getState().updateToolResult(
              id,
              m.messageId,
              m.isError ? 'error' : 'success',
            );
          } else {
            const parsedAtts = m.attachments ? safeParseAttachments(m.attachments) : undefined;
            converted.push({
              id: m.id,
              serverId: m.id,
              role: m.role === 'user' ? 'user' : 'assistant',
              type: 'text',
              content: m.content,
              timestamp,
              ...(parsedAtts ? { attachments: parsedAtts } : {}),
            });
          }
        }
        useWorkspaceChatStore.getState().setHistoryMessages(id, converted);
      }
      setHasMoreHistory(Boolean(res?.hasMore));
    } catch (err: any) {
      console.warn('load more history failed:', err?.message);
    } finally {
      setLoadingMore(false);
    }
  }, [id, loadingMore, hasMoreHistory, messages]);

  const pickAndUploadImage = useCallback(async () => {
    if (!id) return;
    const perm = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (!perm.granted) {
      Alert.alert('需要相册权限', '请在设置中允许访问相册以选择图片');
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: 'images',
      quality: 0.85,
      allowsMultipleSelection: false,
    });
    if (result.canceled || !result.assets?.length) return;
    const asset = result.assets[0];

    setUploadingCount((c) => c + 1);
    try {
      const formData = new FormData();
      const filename =
        asset.fileName ?? `image-${Date.now()}.${asset.uri.split('.').pop() ?? 'jpg'}`;
      formData.append('file', {
        uri: asset.uri,
        name: filename,
        type: asset.mimeType ?? 'image/jpeg',
      } as any);
      const res = await apiUpload<AttachmentUploadResponse>(
        `/workspaces/${id}/attachments`,
        formData,
      );
      setAttachments((prev) => [
        ...prev,
        {
          path: res.path,
          name: res.name,
          mimeType: res.mimeType,
          size: res.size,
          type: 'upload',
          localUri: asset.uri,
        },
      ]);
    } catch (err: any) {
      Alert.alert('上传失败', err?.message ?? '无法上传图片');
    } finally {
      setUploadingCount((c) => c - 1);
    }
  }, [id]);

  const removeAttachment = useCallback((path: string) => {
    setAttachments((prev) => prev.filter((a) => a.path !== path));
  }, []);

  // ─── Menu actions ───────────────────────────────────────────────

  const openTasks = useCallback(async () => {
    setShowMenu(false);
    setTasks(null);
    tasksSheetRef.current?.present();
    if (!id) return;
    try {
      const res = await api.get<{ tasks: WorkspaceTask[] }>(`/workspaces/${id}/workspace-tasks`);
      setTasks(res?.tasks ?? []);
    } catch (err: any) {
      setTasks([]);
      console.warn('load tasks failed:', err?.message);
    }
  }, [id]);

  const openQueue = useCallback(async () => {
    setShowMenu(false);
    setQueue(null);
    queueSheetRef.current?.present();
    if (!id) return;
    try {
      const items = await api.get<QueueItem[]>(`/workspaces/${id}/queue`);
      setQueue(items ?? []);
    } catch (err: any) {
      setQueue([]);
      console.warn('load queue failed:', err?.message);
    }
  }, [id]);

  const openIssue = useCallback(() => {
    setShowMenu(false);
    if (workspace?.issue_id != null) setIssueSheetOpen(true);
  }, [workspace?.issue_id]);

  const handleClearSession = useCallback(async () => {
    if (!id) return;
    try {
      await api.post(`/workspaces/${id}/session/clear`);
      // clearMessages also wipes the per-workspace cursor, so the next
      // useWorkspaceSSE poll falls through to the cold-start `?limit=20`
      // path and repopulates the visible chat.
      useWorkspaceChatStore.getState().clearMessages(id);
    } catch (err: any) {
      Alert.alert('清空失败', err?.message ?? '无法清空对话');
    }
  }, [id]);

  const handleComplete = useCallback(async () => {
    if (!id) return;
    try {
      await api.post(`/workspaces/${id}/complete`);
      router.back();
    } catch (err: any) {
      Alert.alert('完成失败', err?.message ?? '无法标记为完成');
    }
  }, [id, router]);

  // ─── Bottom sheet config ────────────────────────────────────────

  const snapPoints = useMemo(() => ['60%', '90%'], []);
  const renderBackdrop = useCallback(
    (props: BottomSheetBackdropProps) => (
      <BottomSheetBackdrop {...props} disappearsOnIndex={-1} appearsOnIndex={0} opacity={0.5} />
    ),
    [],
  );

  // ─── Render guards ──────────────────────────────────────────────

  if (loading) {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
        <Stack.Screen options={{ headerShown: false }} />
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={colors.brand.accent} />
        </View>
      </View>
    );
  }

  if (!workspace) {
    return (
      <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
        <Stack.Screen options={{ headerShown: false }} />
        <View style={styles.loadingContainer}>
          <Text style={{ color: colors.text.primary }}>未找到工作空间</Text>
        </View>
      </View>
    );
  }

  const status = agentStatusLabel(agentStatus);

  return (
    <View style={[styles.container, { backgroundColor: colors.bg.base, paddingTop: insets.top }]}>
      <Stack.Screen options={{ headerShown: false }} />

      {/* Header */}
      <View style={[styles.header, { borderBottomColor: colors.border.default }]}>
        <Pressable onPress={() => router.back()} hitSlop={8} style={styles.headerIcon}>
          <Ionicons name="chevron-back" size={24} color={colors.text.primary} />
        </Pressable>
        <View style={styles.headerTitle}>
          <Text style={[styles.title, { color: colors.text.primary }]} numberOfLines={1}>
            {workspace.name}
          </Text>
          <StatusBadge status={status} />
        </View>
        <Pressable
          onPress={() => setShowMenu((v) => !v)}
          hitSlop={8}
          style={styles.headerIcon}
          accessibilityRole="button"
          accessibilityLabel="更多操作"
        >
          <Ionicons name="ellipsis-horizontal" size={22} color={colors.text.secondary} />
        </Pressable>
      </View>

      {/* Meta row */}
      {(workspace.project_name ||
        (workspace.changes_count != null && workspace.changes_count > 0) ||
        (workspace.ahead_count != null && workspace.ahead_count > 0)) && (
        <View style={styles.metaRow}>
          {workspace.project_name && (
            <Text style={[styles.metaText, { color: colors.text.secondary }]} numberOfLines={1}>
              📁 {workspace.project_name}
            </Text>
          )}
          {workspace.changes_count != null && workspace.changes_count > 0 && (
            <Text style={[styles.metaText, { color: colors.status.warning }]}>
              ● {workspace.changes_count} 变更
            </Text>
          )}
          {workspace.ahead_count != null && workspace.ahead_count > 0 && (
            <Text style={[styles.metaText, { color: colors.brand.accent }]}>
              ↑ {workspace.ahead_count}
            </Text>
          )}
        </View>
      )}

      {/* Chat */}
      <KeyboardAvoidingView
        style={styles.chatContainer}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      >
        <FlatList
          data={groupedReversed}
          keyExtractor={(item) =>
            Array.isArray(item) ? `tg-${item[0].id}-${item.length}` : item.id
          }
          inverted
          renderItem={({ item }) =>
            Array.isArray(item) ? (
              <ToolGroupCard tools={item} />
            ) : (
              <MessageRow
                message={item}
                workspaceId={id ?? ''}
                onImagePress={(uri) => setLightboxUri(uri)}
              />
            )
          }
          contentContainerStyle={styles.chatContent}
          ListHeaderComponent={typing ? <TypingIndicator /> : null}
          // For inverted lists onEndReached fires near the end of the
          // backing array, which visually corresponds to the top (older
          // messages). 0.3 = trigger when within 30% of the end.
          onEndReached={loadMoreHistory}
          onEndReachedThreshold={0.3}
          // ListFooterComponent on an inverted list renders at the visual
          // top, so the spinner appears above the oldest message.
          ListFooterComponent={
            loadingMore ? (
              <View style={styles.loadMore}>
                <ActivityIndicator size="small" color={colors.text.tertiary} />
                <Text style={[typography.caption, { color: colors.text.tertiary }]}>
                  加载历史消息…
                </Text>
              </View>
            ) : null
          }
          ListEmptyComponent={
            chatLoading ? (
              <View style={styles.empty}>
                <ActivityIndicator size="small" color={colors.brand.accent} />
                <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
                  加载消息中…
                </Text>
              </View>
            ) : (
              <View style={styles.empty}>
                <Ionicons name="chatbubble-ellipses-outline" size={36} color={colors.text.tertiary} />
                <Text style={[styles.emptyText, { color: colors.text.tertiary }]}>
                  发送消息开始对话
                </Text>
              </View>
            )
          }
        />

        {/* Attachment strip */}
        {(attachments.length > 0 || uploadingCount > 0) && (
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={[
              styles.attachStrip,
              { borderTopColor: colors.border.default, backgroundColor: colors.bg.base },
            ]}
          >
            {attachments.map((a) => (
              <AttachmentChip
                key={a.path}
                attachment={a}
                onRemove={() => removeAttachment(a.path)}
              />
            ))}
            {uploadingCount > 0 && (
              <View style={[styles.attachUploading, { backgroundColor: colors.bg.muted }]}>
                <ActivityIndicator size="small" color={colors.brand.accent} />
              </View>
            )}
          </ScrollView>
        )}

        {/* Input bar */}
        <View
          style={[
            styles.inputBar,
            {
              borderTopColor: colors.border.default,
              backgroundColor: colors.bg.base,
              paddingBottom: Math.max(insets.bottom, spacing.sm),
            },
          ]}
        >
          <Pressable
            onPress={pickAndUploadImage}
            hitSlop={6}
            disabled={sending}
            style={styles.attachBtn}
            accessibilityRole="button"
            accessibilityLabel="添加图片"
          >
            <Ionicons name="image-outline" size={22} color={colors.text.secondary} />
          </Pressable>
          <TextInput
            style={[
              styles.input,
              {
                backgroundColor: colors.bg.muted,
                color: colors.text.primary,
                borderColor: colors.border.default,
              },
            ]}
            value={input}
            onChangeText={setInput}
            placeholder="输入消息…"
            placeholderTextColor={colors.text.tertiary}
            multiline
            maxLength={4000}
          />
          <Pressable
            onPress={handleSend}
            disabled={(!input.trim() && attachments.length === 0) || sending || uploadingCount > 0}
            hitSlop={6}
            style={[
              styles.sendBtn,
              {
                backgroundColor:
                  (input.trim() || attachments.length > 0) && uploadingCount === 0
                    ? colors.brand.accent
                    : colors.bg.muted,
              },
            ]}
          >
            {sending ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Ionicons
                name="arrow-up"
                size={18}
                color={
                  (input.trim() || attachments.length > 0) && uploadingCount === 0
                    ? '#fff'
                    : colors.text.tertiary
                }
              />
            )}
          </Pressable>
        </View>
      </KeyboardAvoidingView>

      {/* Dropdown menu */}
      {showMenu && (
        <View style={styles.dropdownOverlay}>
          <Pressable style={styles.dropdownBackdrop} onPress={() => setShowMenu(false)} />
          <View
            style={[
              styles.dropdown,
              {
                top: insets.top + 52,
                backgroundColor: colors.bg.surface,
                borderColor: colors.border.default,
              },
            ]}
          >
            {workspace.issue_id != null && (
              <MenuItem
                icon="link-outline"
                label={workspace.issue_title ?? `Issue #${workspace.issue_id}`}
                onPress={openIssue}
              />
            )}
            <MenuItem icon="checkmark-circle-outline" label="任务列表" onPress={openTasks} />
            <MenuItem icon="list-outline" label="执行队列" onPress={openQueue} />
            <MenuItem
              icon="refresh-outline"
              label="清空对话"
              onPress={() => {
                setShowMenu(false);
                setConfirm('clear');
              }}
            />
            <View style={[styles.menuDivider, { backgroundColor: colors.border.subtle }]} />
            <MenuItem
              icon="checkmark-done-outline"
              label="完成工作空间"
              onPress={() => {
                setShowMenu(false);
                setConfirm('complete');
              }}
            />
          </View>
        </View>
      )}

      {/* Tasks bottom sheet */}
      <BottomSheetModal
        ref={tasksSheetRef}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        backgroundStyle={{ backgroundColor: colors.bg.surface }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView style={styles.sheetHeader}>
          <Text style={[styles.sheetTitle, { color: colors.text.primary }]}>任务列表</Text>
        </BottomSheetView>
        {tasks == null ? (
          <View style={styles.sheetLoading}>
            <ActivityIndicator size="small" color={colors.brand.accent} />
          </View>
        ) : tasks.length === 0 ? (
          <View style={styles.sheetEmpty}>
            <Text style={{ color: colors.text.tertiary }}>暂无任务</Text>
          </View>
        ) : (
          <BottomSheetFlatList
            data={tasks}
            keyExtractor={(t) => String(t.id)}
            contentContainerStyle={styles.sheetList}
            renderItem={({ item }) => <TaskRow task={item} />}
          />
        )}
      </BottomSheetModal>

      {/* Queue bottom sheet */}
      <BottomSheetModal
        ref={queueSheetRef}
        snapPoints={snapPoints}
        backdropComponent={renderBackdrop}
        backgroundStyle={{ backgroundColor: colors.bg.surface }}
        handleIndicatorStyle={{ backgroundColor: colors.border.default, width: 36 }}
      >
        <BottomSheetView style={styles.sheetHeader}>
          <Text style={[styles.sheetTitle, { color: colors.text.primary }]}>执行队列</Text>
        </BottomSheetView>
        {queue == null ? (
          <View style={styles.sheetLoading}>
            <ActivityIndicator size="small" color={colors.brand.accent} />
          </View>
        ) : queue.length === 0 ? (
          <View style={styles.sheetEmpty}>
            <Text style={{ color: colors.text.tertiary }}>队列为空</Text>
          </View>
        ) : (
          <BottomSheetFlatList
            data={queue}
            keyExtractor={(q) => String(q.id)}
            contentContainerStyle={styles.sheetList}
            renderItem={({ item }) => <QueueRow item={item} />}
          />
        )}
      </BottomSheetModal>

      {/* Issue detail sheet */}
      {workspace.issue_id != null && issueSheetOpen && (
        <IssueDetailSheet
          issueId={workspace.issue_id}
          visible={issueSheetOpen}
          onClose={() => setIssueSheetOpen(false)}
        />
      )}

      {/* Confirm dialogs */}
      <ConfirmDialog
        visible={confirm === 'clear'}
        title="清空对话"
        message="将清空当前 Agent 会话和本地消息历史。此操作不可撤销。"
        confirmLabel="清空"
        destructive
        onConfirm={handleClearSession}
        onCancel={() => setConfirm(null)}
      />
      <ConfirmDialog
        visible={confirm === 'complete'}
        title="完成工作空间"
        message="标记此工作空间为已完成并返回。此操作不会删除工作空间内容。"
        confirmLabel="完成"
        destructive={false}
        onConfirm={handleComplete}
        onCancel={() => setConfirm(null)}
      />

      {/* Image lightbox: full-screen tap-to-dismiss preview. resizeMode
          contain so portrait/landscape images both fit without crop. */}
      <Modal
        visible={lightboxUri != null}
        transparent
        animationType="fade"
        onRequestClose={() => setLightboxUri(null)}
      >
        <Pressable
          style={styles.lightboxBackdrop}
          onPress={() => setLightboxUri(null)}
        >
          {lightboxUri ? (
            <Image
              source={{ uri: lightboxUri }}
              style={styles.lightboxImage}
              resizeMode="contain"
            />
          ) : null}
          <Pressable
            onPress={() => setLightboxUri(null)}
            hitSlop={12}
            style={[styles.lightboxClose, { top: insets.top + spacing.md }]}
          >
            <Ionicons name="close" size={28} color="#fff" />
          </Pressable>
        </Pressable>
      </Modal>
    </View>
  );
}

// ─── Sub-components ─────────────────────────────────────────────────

function MenuItem({
  icon,
  label,
  onPress,
}: {
  icon: React.ComponentProps<typeof Ionicons>['name'];
  label: string;
  onPress: () => void;
}) {
  const colors = useThemeColors();
  return (
    <TouchableOpacity style={styles.menuItem} onPress={onPress}>
      <Ionicons name={icon} size={18} color={colors.text.primary} />
      <Text style={[styles.menuItemText, { color: colors.text.primary }]} numberOfLines={1}>
        {label}
      </Text>
    </TouchableOpacity>
  );
}

function MessageRow({
  message,
  workspaceId,
  onImagePress,
}: {
  message: ChatMessage;
  workspaceId: string;
  onImagePress: (uri: string) => void;
}) {
  const colors = useThemeColors();
  const mdStyles = useMemo(() => buildMarkdownStyles(colors), [colors]);

  if (message.type === 'tool_use') {
    return <ToolUseCard message={message} />;
  }

  const isUser = message.role === 'user';
  const time = formatChatTime(message.timestamp);
  // Strip server-appended "[附件: ...]" suffix when there's an attachments
  // array — the chips below convey the same information visually.
  const visibleContent = stripAttachmentSuffix(message.content, message.attachments);

  if (isUser) {
    // SPA pattern: subtle blue-tinted block + left border accent + "You" label.
    // User input stays plain text (matches SPA chat-message.tsx — user typing
    // "# heading" shouldn't render as a heading).
    return (
      <View
        style={[
          styles.userBlock,
          { backgroundColor: colors.brand.accentLight, borderLeftColor: colors.brand.accent },
        ]}
      >
        <View style={styles.msgHeader}>
          <Text style={[styles.msgRoleUser, { color: colors.brand.accent }]}>You</Text>
          {time ? (
            <Text style={[styles.msgTime, { color: colors.text.tertiary }]}>{time}</Text>
          ) : null}
        </View>
        {message.attachments && message.attachments.length > 0 && (
          <View style={styles.attachmentList}>
            {message.attachments.map((a) => (
              <MessageAttachment
                key={a.path}
                attachment={a}
                workspaceId={workspaceId}
                onPress={onImagePress}
              />
            ))}
          </View>
        )}
        {visibleContent ? (
          <Text style={[typography.body, { color: colors.text.primary }]}>
            {visibleContent}
          </Text>
        ) : null}
      </View>
    );
  }

  // Assistant: markdown-rendered content + green "🐂Niuniu(claude)" label.
  return (
    <View style={styles.assistantBlock}>
      <View style={styles.msgHeader}>
        <Text style={[styles.msgRoleAssistant, { color: colors.status.success }]}>
          🐂Niuniu(claude)
        </Text>
        {time ? (
          <Text style={[styles.msgTime, { color: colors.text.tertiary }]}>{time}</Text>
        ) : null}
      </View>
      <Markdown style={mdStyles}>{message.content}</Markdown>
    </View>
  );
}

// Single attachment chip rendered inside a user message. Mirrors SPA
// attachment-preview.tsx visually: image → 200x130 thumbnail with name +
// size + "click to zoom"; non-image → 📎 + name + size card.
function MessageAttachment({
  attachment,
  workspaceId,
  onPress,
}: {
  attachment: ChatAttachment;
  workspaceId: string;
  onPress: (uri: string) => void;
}) {
  const colors = useThemeColors();
  const isImage = isImageAttachment(attachment);
  const sizeText = formatBytes(attachment.size);

  if (isImage) {
    // Prefer server URL (for messages from history that we just paged in,
    // we don't have a localUri). Fall back to localUri (optimistic insert
    // right after upload) when present. Falls through to the file card
    // when neither is available (e.g. relay mode, no server URL).
    const serverUrl = getFileContentUrl(workspaceId, attachment.path);
    const uri = serverUrl ?? attachment.localUri ?? null;
    if (uri) {
      return (
        <Pressable
          onPress={() => onPress(uri)}
          style={[
            styles.attachImageCard,
            { borderColor: colors.border.default, backgroundColor: colors.bg.surface },
          ]}
        >
          <Image
            source={{ uri }}
            style={[styles.attachImageThumb, { backgroundColor: colors.bg.muted }]}
          />
          <View style={styles.attachImageMeta}>
            <Text
              style={[typography.caption, { color: colors.text.primary }]}
              numberOfLines={1}
            >
              {attachment.name}
            </Text>
            <Text style={[styles.attachSize, { color: colors.text.tertiary }]}>
              {sizeText} · 点击放大
            </Text>
          </View>
        </Pressable>
      );
    }
    // No URL available — fall through to the file card so the user at
    // least sees that an image was attached.
  }

  return (
    <View
      style={[
        styles.attachFileCard,
        { borderColor: colors.border.default, backgroundColor: colors.bg.surface },
      ]}
    >
      <Text style={styles.attachEmoji}>📎</Text>
      <View style={{ flex: 1 }}>
        <Text
          style={[typography.caption, { color: colors.text.primary }]}
          numberOfLines={1}
        >
          {attachment.name}
        </Text>
        {sizeText ? (
          <Text style={[styles.attachSize, { color: colors.text.tertiary }]}>{sizeText}</Text>
        ) : null}
      </View>
    </View>
  );
}

// Markdown style sheet keyed by the markdown-it token names that
// react-native-markdown-display recognises. Themes follow the existing
// palette so light/dark mode both stay readable without separate sheets.
function buildMarkdownStyles(colors: ReturnType<typeof useThemeColors>) {
  const mono = Platform.OS === 'ios' ? 'Menlo' : 'monospace';
  return StyleSheet.create({
    body: { color: colors.text.primary, fontSize: 15, lineHeight: 22 },
    paragraph: { marginTop: 0, marginBottom: spacing.sm },
    heading1: { fontSize: 20, fontWeight: '700', marginTop: spacing.md, marginBottom: spacing.sm, color: colors.text.primary },
    heading2: { fontSize: 18, fontWeight: '700', marginTop: spacing.md, marginBottom: spacing.sm, color: colors.text.primary },
    heading3: { fontSize: 16, fontWeight: '600', marginTop: spacing.sm, marginBottom: spacing.xs, color: colors.text.primary },
    heading4: { fontSize: 15, fontWeight: '600', marginTop: spacing.sm, marginBottom: spacing.xs, color: colors.text.primary },
    strong: { fontWeight: '700' },
    em: { fontStyle: 'italic' },
    link: { color: colors.brand.accent, textDecorationLine: 'underline' },
    bullet_list: { marginVertical: spacing.xs },
    ordered_list: { marginVertical: spacing.xs },
    list_item: { marginVertical: 2 },
    bullet_list_icon: { color: colors.text.secondary, marginRight: 6, lineHeight: 22 },
    ordered_list_icon: { color: colors.text.secondary, marginRight: 6, lineHeight: 22 },
    code_inline: {
      fontFamily: mono,
      backgroundColor: colors.bg.muted,
      color: colors.text.primary,
      paddingHorizontal: 4,
      borderRadius: 3,
    },
    code_block: {
      fontFamily: mono,
      backgroundColor: colors.bg.muted,
      color: colors.text.primary,
      padding: spacing.sm,
      borderRadius: radius.sm,
      marginVertical: spacing.xs,
      fontSize: 13,
      lineHeight: 18,
    },
    fence: {
      fontFamily: mono,
      backgroundColor: colors.bg.muted,
      color: colors.text.primary,
      padding: spacing.sm,
      borderRadius: radius.sm,
      marginVertical: spacing.xs,
      fontSize: 13,
      lineHeight: 18,
    },
    blockquote: {
      borderLeftWidth: 3,
      borderLeftColor: colors.border.default,
      backgroundColor: colors.bg.surface,
      paddingHorizontal: spacing.md,
      paddingVertical: spacing.xs,
      marginVertical: spacing.xs,
    },
    hr: { backgroundColor: colors.border.default, height: StyleSheet.hairlineWidth, marginVertical: spacing.sm },
    table: { borderWidth: 1, borderColor: colors.border.default, borderRadius: radius.xs },
    thead: { backgroundColor: colors.bg.muted },
    th: { padding: spacing.xs, fontWeight: '600' },
    td: { padding: spacing.xs },
  });
}

function ToolGroupCard({ tools }: { tools: ChatMessage[] }) {
  const colors = useThemeColors();
  const [expanded, setExpanded] = useState(false);

  const completedCount = tools.filter((t) => t.toolStatus != null).length;
  const allDone = completedCount === tools.length;
  const hasError = tools.some((t) => t.toolStatus === 'error');
  const headerColor = hasError
    ? colors.status.error
    : allDone
      ? colors.status.success
      : colors.text.tertiary;

  // Distinct tool names, preserving order, capped at the first few to avoid
  // a header line that wraps to 4 rows when an agent fires 20+ tools.
  const distinctNames: string[] = [];
  for (const t of tools) {
    const name = t.toolName ?? 'tool';
    if (!distinctNames.includes(name)) distinctNames.push(name);
    if (distinctNames.length >= 4) break;
  }
  const moreCount = new Set(tools.map((t) => t.toolName ?? 'tool')).size - distinctNames.length;
  const namesLabel =
    distinctNames.join(', ') + (moreCount > 0 ? ` +${moreCount}` : '');

  return (
    <View
      style={[
        styles.toolGroup,
        { backgroundColor: colors.bg.surface, borderColor: colors.border.default },
      ]}
    >
      <Pressable onPress={() => setExpanded((v) => !v)} style={styles.toolGroupHeader}>
        <Ionicons
          name={expanded ? 'chevron-down' : 'chevron-forward'}
          size={14}
          color={colors.text.tertiary}
        />
        <Ionicons name="construct-outline" size={14} color={headerColor} />
        <Text style={[styles.toolGroupTitle, { color: colors.text.primary }]} numberOfLines={1}>
          {tools.length} 个工具
        </Text>
        <Text
          style={[styles.toolGroupNames, { color: colors.text.tertiary }]}
          numberOfLines={1}
        >
          ({namesLabel})
        </Text>
      </Pressable>
      {!expanded && (
        <View style={styles.toolGroupSummary}>
          <Text style={[styles.toolGroupBranch, { color: colors.text.tertiary }]}>⎿</Text>
          <Text
            style={[
              typography.caption,
              {
                color: hasError
                  ? colors.status.error
                  : allDone
                    ? colors.text.secondary
                    : colors.brand.accent,
              },
            ]}
          >
            {allDone
              ? `完成 (${completedCount} 个)`
              : `${completedCount}/${tools.length} 已完成…`}
          </Text>
        </View>
      )}
      {expanded && (
        <View style={[styles.toolGroupBody, { borderLeftColor: colors.border.default }]}>
          {tools.map((t) => (
            <ToolUseCard key={t.id} message={t} embedded />
          ))}
        </View>
      )}
    </View>
  );
}

function ToolUseCard({ message, embedded }: { message: ChatMessage; embedded?: boolean }) {
  const colors = useThemeColors();
  const [expanded, setExpanded] = useState(false);

  let statusColor: string = colors.text.tertiary;
  if (message.toolStatus === 'error') statusColor = colors.status.error;
  else if (message.toolStatus === 'success') statusColor = colors.status.success;

  return (
    <Pressable
      onPress={() => setExpanded((v) => !v)}
      style={[
        embedded ? styles.toolCardEmbedded : styles.toolCard,
        embedded
          ? null
          : { backgroundColor: colors.bg.surface, borderColor: colors.border.default },
      ]}
    >
      <View style={styles.toolHeader}>
        {!embedded && <Ionicons name="construct-outline" size={14} color={statusColor} />}
        <Text style={[styles.toolName, { color: colors.text.primary }]} numberOfLines={1}>
          {message.toolName ?? 'tool'}
        </Text>
        {message.toolStatus && (
          <View style={[styles.toolStatusDot, { backgroundColor: statusColor }]} />
        )}
        <Ionicons
          name={expanded ? 'chevron-up' : 'chevron-down'}
          size={14}
          color={colors.text.tertiary}
        />
      </View>
      {expanded && message.toolInput ? (
        <Text
          style={[styles.toolInput, { color: colors.text.secondary }]}
          numberOfLines={10}
        >
          {message.toolInput}
        </Text>
      ) : null}
    </Pressable>
  );
}

function TypingIndicator() {
  const colors = useThemeColors();
  return (
    <View style={styles.typing}>
      <View style={[styles.typingDot, { backgroundColor: colors.text.tertiary }]} />
      <View style={[styles.typingDot, { backgroundColor: colors.text.tertiary }]} />
      <View style={[styles.typingDot, { backgroundColor: colors.text.tertiary }]} />
    </View>
  );
}

function TaskRow({ task }: { task: WorkspaceTask }) {
  const colors = useThemeColors();
  const statusColor =
    task.status === 'completed'
      ? colors.status.success
      : task.status === 'in_progress'
        ? colors.brand.accent
        : task.status === 'failed' || task.status === 'interrupted'
          ? colors.status.error
          : colors.text.tertiary;
  return (
    <View style={[styles.taskRow, { borderBottomColor: colors.border.subtle }]}>
      <View style={[styles.taskDot, { backgroundColor: statusColor }]} />
      <View style={{ flex: 1 }}>
        <Text style={[typography.bodyMedium, { color: colors.text.primary }]} numberOfLines={2}>
          {task.subject}
        </Text>
        {task.description ? (
          <Text
            style={[typography.caption, { color: colors.text.tertiary, marginTop: 2 }]}
            numberOfLines={2}
          >
            {task.description}
          </Text>
        ) : null}
      </View>
      <Text style={[typography.caption, { color: colors.text.tertiary }]}>{task.status}</Text>
    </View>
  );
}

function AttachmentChip({
  attachment,
  onRemove,
}: {
  attachment: ChatAttachment;
  onRemove: () => void;
}) {
  const colors = useThemeColors();
  const isImage = attachment.mimeType.startsWith('image/');

  return (
    <View style={[styles.attachChip, { backgroundColor: colors.bg.muted }]}>
      {isImage && attachment.localUri ? (
        <Image source={{ uri: attachment.localUri }} style={styles.attachThumb} />
      ) : (
        <View style={[styles.attachThumb, styles.attachIconBox]}>
          <Ionicons
            name={isImage ? 'image-outline' : 'document-outline'}
            size={20}
            color={colors.text.secondary}
          />
        </View>
      )}
      <Pressable onPress={onRemove} hitSlop={6} style={styles.attachRemove}>
        <Ionicons name="close" size={14} color="#fff" />
      </Pressable>
    </View>
  );
}

function QueueRow({ item }: { item: QueueItem }) {
  const colors = useThemeColors();
  return (
    <View style={[styles.queueRow, { borderBottomColor: colors.border.subtle }]}>
      <Text style={[styles.queuePos, { color: colors.text.tertiary }]}>#{item.position + 1}</Text>
      <Text style={[typography.body, { color: colors.text.primary, flex: 1 }]} numberOfLines={3}>
        {item.content}
      </Text>
    </View>
  );
}

// ─── Styles ─────────────────────────────────────────────────────────

const styles = StyleSheet.create({
  container: { flex: 1 },
  loadingContainer: { flex: 1, alignItems: 'center', justifyContent: 'center' },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  headerIcon: {
    width: 36,
    height: 36,
    alignItems: 'center',
    justifyContent: 'center',
  },
  headerTitle: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
  },
  title: { ...typography.sectionHead, flexShrink: 1 },
  metaRow: {
    flexDirection: 'row',
    paddingHorizontal: spacing.lg,
    paddingTop: spacing.xs,
    paddingBottom: spacing.sm,
    gap: spacing.md,
    flexWrap: 'wrap',
  },
  metaText: { ...typography.caption },

  chatContainer: { flex: 1 },
  chatContent: { padding: spacing.lg, gap: spacing.md, flexGrow: 1 },
  empty: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    gap: spacing.md,
    paddingVertical: spacing['3xl'],
  },
  emptyText: { ...typography.body },

  userBlock: {
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    borderLeftWidth: 2,
    borderRadius: radius.sm,
  },
  assistantBlock: {
    paddingVertical: spacing.xs,
  },
  msgHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    marginBottom: spacing.xs,
  },
  msgRoleUser: { ...typography.label, fontWeight: '600' },
  msgRoleAssistant: { ...typography.label, fontWeight: '600' },
  msgTime: { ...typography.caption },

  toolCard: {
    borderWidth: 1,
    borderRadius: radius.sm,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    maxWidth: '90%',
    alignSelf: 'flex-start',
  },
  toolCardEmbedded: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 4,
  },
  toolHeader: { flexDirection: 'row', alignItems: 'center', gap: spacing.sm },
  toolName: { ...typography.label, flex: 1 },
  toolStatusDot: { width: 6, height: 6, borderRadius: 3 },
  toolInput: {
    ...typography.caption,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
    marginTop: spacing.sm,
  },

  toolGroup: {
    borderWidth: 1,
    borderRadius: radius.sm,
    maxWidth: '90%',
    alignSelf: 'flex-start',
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
  },
  toolGroupHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
  },
  toolGroupTitle: { ...typography.label, fontWeight: '600' },
  toolGroupNames: { ...typography.caption, flex: 1 },
  toolGroupSummary: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.xs,
    paddingLeft: 18,
    paddingTop: 2,
  },
  toolGroupBranch: { ...typography.caption },
  toolGroupBody: {
    marginTop: spacing.xs,
    marginLeft: 8,
    paddingLeft: spacing.sm,
    borderLeftWidth: 1,
    gap: 2,
  },

  attachStrip: {
    flexDirection: 'row',
    gap: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingTop: spacing.sm,
    paddingBottom: spacing.xs,
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  attachChip: {
    width: 56,
    height: 56,
    borderRadius: radius.sm,
    overflow: 'visible',
  },
  attachThumb: {
    width: 56,
    height: 56,
    borderRadius: radius.sm,
  },
  attachIconBox: { alignItems: 'center', justifyContent: 'center' },
  attachRemove: {
    position: 'absolute',
    top: -6,
    right: -6,
    width: 20,
    height: 20,
    borderRadius: 10,
    backgroundColor: 'rgba(0,0,0,0.6)',
    alignItems: 'center',
    justifyContent: 'center',
  },
  attachUploading: {
    width: 56,
    height: 56,
    borderRadius: radius.sm,
    alignItems: 'center',
    justifyContent: 'center',
  },

  attachBtn: {
    width: 36,
    height: 36,
    alignItems: 'center',
    justifyContent: 'center',
  },

  // Attachment chips inside a rendered user message
  attachmentList: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: spacing.sm,
    marginBottom: spacing.sm,
  },
  attachImageCard: {
    width: 200,
    borderRadius: radius.sm,
    borderWidth: 1,
    overflow: 'hidden',
  },
  attachImageThumb: {
    width: 200,
    height: 130,
  },
  attachImageMeta: {
    paddingHorizontal: spacing.sm,
    paddingVertical: 6,
  },
  attachFileCard: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    borderRadius: radius.sm,
    borderWidth: 1,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    maxWidth: '90%',
  },
  attachEmoji: { fontSize: 18 },
  attachSize: {
    ...typography.caption,
    fontSize: 10,
    marginTop: 1,
  },

  // Load-more spinner shown above oldest message in inverted FlatList
  loadMore: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: spacing.sm,
    paddingVertical: spacing.md,
  },

  // Lightbox
  lightboxBackdrop: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.95)',
    justifyContent: 'center',
    alignItems: 'center',
  },
  lightboxImage: {
    width: '100%',
    height: '100%',
  },
  lightboxClose: {
    position: 'absolute',
    right: spacing.md,
    width: 36,
    height: 36,
    borderRadius: 18,
    backgroundColor: 'rgba(0,0,0,0.5)',
    alignItems: 'center',
    justifyContent: 'center',
  },

  inputBar: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    gap: spacing.sm,
    paddingHorizontal: spacing.md,
    paddingTop: spacing.sm,
    borderTopWidth: StyleSheet.hairlineWidth,
  },
  input: {
    flex: 1,
    borderWidth: 1,
    borderRadius: radius.lg,
    paddingHorizontal: spacing.md,
    paddingVertical: spacing.sm,
    maxHeight: 120,
    minHeight: 40,
    ...typography.body,
  },
  sendBtn: {
    width: 36,
    height: 36,
    borderRadius: 18,
    alignItems: 'center',
    justifyContent: 'center',
  },

  typing: { flexDirection: 'row', gap: 4, padding: spacing.md },
  typingDot: { width: 6, height: 6, borderRadius: 3, opacity: 0.5 },

  // Dropdown menu
  dropdownOverlay: {
    position: 'absolute',
    top: 0,
    right: 0,
    left: 0,
    bottom: 0,
    zIndex: 100,
  },
  dropdownBackdrop: {
    ...StyleSheet.absoluteFillObject,
  },
  dropdown: {
    position: 'absolute',
    right: spacing.md,
    minWidth: 200,
    borderRadius: radius.md,
    borderWidth: 1,
    paddingVertical: spacing.xs,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.15,
    shadowRadius: 12,
    elevation: 8,
  },
  menuItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: spacing.lg,
    paddingVertical: spacing.md,
    gap: spacing.md,
  },
  menuItemText: { ...typography.body, flexShrink: 1 },
  menuDivider: { height: StyleSheet.hairlineWidth, marginVertical: 2 },

  // Bottom sheets
  sheetHeader: {
    paddingHorizontal: spacing.lg,
    paddingTop: spacing.sm,
    paddingBottom: spacing.md,
  },
  sheetTitle: { ...typography.sectionHead },
  sheetLoading: { paddingVertical: spacing['2xl'], alignItems: 'center' },
  sheetEmpty: { paddingVertical: spacing['2xl'], alignItems: 'center' },
  sheetList: { paddingHorizontal: spacing.lg, paddingBottom: spacing['3xl'] },

  taskRow: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: spacing.md,
    paddingVertical: spacing.md,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  taskDot: { width: 8, height: 8, borderRadius: 4, marginTop: 6 },

  queueRow: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: spacing.md,
    paddingVertical: spacing.md,
    borderBottomWidth: StyleSheet.hairlineWidth,
  },
  queuePos: { ...typography.label, minWidth: 32 },
});
