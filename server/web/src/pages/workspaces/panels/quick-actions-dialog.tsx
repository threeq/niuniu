import { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import { Plus, Trash2, GripVertical, Pencil, Check, X } from 'lucide-react';
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog';
import { useQuickActionsStore, type QuickAction } from '@/stores/quick-actions-store';
import { OwnerPicker } from '@/components/shared/owner-picker';
import { useAuthStore } from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import { groupByOwner } from '@/lib/owner-group';
import type { OwnerRef } from '@/types/org';

interface QuickActionsDialogProps {
  workspaceId?: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function QuickActionsDialog({ workspaceId, open, onOpenChange }: QuickActionsDialogProps) {
  const { t } = useTranslation('workspaces');
  const { actions, sceneActions = [], fetchActions, addAction, updateAction, removeAction, setLocalOrder, persistOrder } = useQuickActionsStore();
  const currentUser = useAuthStore((s) => s.user);
  const myOrgs = useOrgStore((s) => s.myOrgs);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [editForm, setEditForm] = useState({ label: '', content: '', auto_send: false });
  const [addingNew, setAddingNew] = useState(false);
  const [newForm, setNewForm] = useState({ label: '', content: '', auto_send: false });
  const [newOwner, setNewOwner] = useState<OwnerRef>({ type: 'user', id: currentUser?.id ?? 0 });
  const [dragIndex, setDragIndex] = useState<{ groupKey: string; indexInGroup: number } | null>(null);

  useEffect(() => {
    if (open) fetchActions(workspaceId);
  }, [open, fetchActions, workspaceId]);

  const startEdit = (action: QuickAction) => {
    setEditingId(action.id);
    setEditForm({ label: action.label, content: action.content, auto_send: action.auto_send });
    setAddingNew(false);
  };

  const saveEdit = async () => {
    if (editingId && editForm.label.trim() && editForm.content.trim()) {
      await updateAction(editingId, editForm);
      setEditingId(null);
    }
  };

  const cancelEdit = () => {
    setEditingId(null);
  };

  const handleAdd = async () => {
    if (newForm.label.trim() && newForm.content.trim()) {
      await addAction({ ...newForm, owner: newOwner });
      setNewForm({ label: '', content: '', auto_send: false });
      setNewOwner({ type: 'user', id: currentUser?.id ?? 0 });
      setAddingNew(false);
    }
  };

  const handleDelete = async (id: number) => {
    await removeAction(id);
  };

  const groups = groupByOwner(actions, currentUser?.id ?? 0, myOrgs);
  const showHeaders = myOrgs.length > 0;

  const handleDragStart = (groupKey: string, indexInGroup: number) => {
    setDragIndex({ groupKey, indexInGroup });
  };

  const handleDragOver = (e: React.DragEvent, groupKey: string, indexInGroup: number) => {
    e.preventDefault();
    if (!dragIndex || dragIndex.groupKey !== groupKey || dragIndex.indexInGroup === indexInGroup) return;
    if (editingId !== null) return;
    // Reorder within the group, then rebuild the global actions array
    const target = groups.find((g) => g.key === groupKey);
    if (!target) return;
    const reorderedGroupItems = [...target.items];
    const [moved] = reorderedGroupItems.splice(dragIndex.indexInGroup, 1);
    reorderedGroupItems.splice(indexInGroup, 0, moved);
    const rebuilt: QuickAction[] = groups.flatMap((g) => (g.key === groupKey ? reorderedGroupItems : g.items));
    setLocalOrder(rebuilt);
    setDragIndex({ groupKey, indexInGroup });
  };

  const handleDragEnd = () => {
    setDragIndex(null);
    persistOrder();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-lg max-h-[80vh]">
        <DialogHeader>
          <DialogTitle>{t('panels.quickActions.dialogTitle')}</DialogTitle>
          <DialogDescription>
            {t('panels.quickActions.dialogDescription')}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2">
          {groups.map((group) => (
            <div key={group.key} className="space-y-1">
              {showHeaders && (
                <div
                  data-testid="owner-group-header"
                  className="px-2 pt-2 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/70"
                >
                  {group.label || t('panels.quickActions.groupPersonal')}
                </div>
              )}
              {group.items.map((action, indexInGroup) => (
                <div
                  key={action.id}
                  data-testid={`qa-row-${action.id}`}
                  draggable={editingId !== action.id}
                  onDragStart={() => handleDragStart(group.key, indexInGroup)}
                  onDragOver={(e) => handleDragOver(e, group.key, indexInGroup)}
                  onDragEnd={handleDragEnd}
                  className="group rounded-md border border-border bg-card"
                >
                  {editingId === action.id ? (
                    <div className="p-3 space-y-2">
                      <input
                        type="text"
                        value={editForm.label}
                        onChange={(e) => setEditForm((f) => ({ ...f, label: e.target.value }))}
                        placeholder={t('panels.quickActions.labelPlaceholder')}
                        className="w-full rounded border border-border px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-info"
                        autoFocus
                      />
                      <textarea
                        value={editForm.content}
                        onChange={(e) => setEditForm((f) => ({ ...f, content: e.target.value }))}
                        placeholder={t('panels.quickActions.contentPlaceholder')}
                        rows={2}
                        className="w-full rounded border border-border px-2 py-1 text-sm resize-none focus:outline-none focus:ring-1 focus:ring-info"
                      />
                      <div className="flex items-center justify-between">
                        <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
                          <input
                            type="checkbox"
                            checked={editForm.auto_send}
                            onChange={(e) => setEditForm((f) => ({ ...f, auto_send: e.target.checked }))}
                            className="rounded border-border"
                          />
                          {t('panels.quickActions.autoSendLabel')}
                        </label>
                        <div className="flex items-center gap-1">
                          <button onClick={cancelEdit} className="p-1 text-gray-400 hover:text-gray-600">
                            <X className="h-4 w-4" />
                          </button>
                          <button onClick={saveEdit} className="p-1 text-blue-500 hover:text-blue-700">
                            <Check className="h-4 w-4" />
                          </button>
                        </div>
                      </div>
                    </div>
                  ) : (
                    <div className="flex items-start gap-2 px-2 py-2">
                      <GripVertical className="h-4 w-4 mt-0.5 text-muted-foreground/50 cursor-grab shrink-0" />
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-1.5 flex-wrap">
                          <span className="text-sm font-medium text-foreground break-words">{action.label}</span>
                          {action.auto_send && (
                            <span className="text-[10px] px-1 py-0.5 rounded bg-info/10 text-info shrink-0">
                              {t('panels.quickActions.autoSendBadge')}
                            </span>
                          )}
                        </div>
                        <p className="text-xs text-muted-foreground/70 mt-0.5 break-words whitespace-pre-wrap">{action.content}</p>
                      </div>
                      <div className="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity shrink-0">
                        <button
                          onClick={() => startEdit(action)}
                          className="p-1 text-gray-400 hover:text-blue-500"
                          title={t('panels.quickActions.edit')}
                        >
                          <Pencil className="h-3.5 w-3.5" />
                        </button>
                        <button
                          onClick={() => handleDelete(action.id)}
                          className="p-1 text-gray-400 hover:text-red-500"
                          title={t('panels.quickActions.delete')}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              ))}
            </div>
          ))}
        </div>

        {/* Scene-provided quick actions — read-only, parsed live from the
            workspace's active scene(s); not editable here and never persisted. */}
        {sceneActions.length > 0 && (
          <div className="space-y-1">
            <div
              data-testid="scene-group-header"
              className="px-2 pt-2 pb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground/70"
            >
              {t('panels.quickActions.groupScene')}
            </div>
            {sceneActions.map((action) => (
              <div
                key={`scene:${action.slug}`}
                className="rounded-md border border-border bg-muted/30"
              >
                <div className="flex items-start gap-2 px-2 py-2">
                  <div className="flex-1 min-w-0">
                    <span className="text-sm font-medium text-foreground break-words">{action.label}</span>
                    <p className="text-xs text-muted-foreground/70 mt-0.5 break-words whitespace-pre-wrap">{action.content}</p>
                  </div>
                  <span className="text-[10px] px-1 py-0.5 rounded bg-muted text-muted-foreground shrink-0">
                    {t('panels.quickActions.sceneReadOnly')}
                  </span>
                </div>
              </div>
            ))}
          </div>
        )}

        {addingNew ? (
          <div className="rounded-md border border-dashed border-info/30 bg-info/5 p-3 space-y-2">
            <OwnerPicker
              value={newOwner}
              onChange={setNewOwner}
              userId={currentUser?.id ?? 0}
              autoSelectDefault={true}
            />
            <input
              type="text"
              value={newForm.label}
              onChange={(e) => setNewForm((f) => ({ ...f, label: e.target.value }))}
              placeholder={t('panels.quickActions.labelPlaceholder')}
              className="w-full rounded border border-gray-300 px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-blue-400"
              autoFocus
            />
            <textarea
              value={newForm.content}
              onChange={(e) => setNewForm((f) => ({ ...f, content: e.target.value }))}
              placeholder={t('panels.quickActions.contentPlaceholder')}
              rows={2}
              className="w-full rounded border border-gray-300 px-2 py-1 text-sm resize-none focus:outline-none focus:ring-1 focus:ring-blue-400"
            />
            <div className="flex items-center justify-between">
              <label className="flex items-center gap-1.5 text-xs text-muted-foreground cursor-pointer">
                <input
                  type="checkbox"
                  checked={newForm.auto_send}
                  onChange={(e) => setNewForm((f) => ({ ...f, auto_send: e.target.checked }))}
                  className="rounded border-border"
                />
                {t('panels.quickActions.autoSendLabel')}
              </label>
              <div className="flex items-center gap-1">
                <button
                  onClick={() => { setAddingNew(false); setNewForm({ label: '', content: '', auto_send: false }); }}
                  className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
                >
                  {t('common:actions.cancel')}
                </button>
                <button
                  onClick={handleAdd}
                  disabled={!newForm.label.trim() || !newForm.content.trim()}
                  className="rounded bg-info px-2 py-1 text-xs text-white hover:bg-info/90 disabled:opacity-50"
                >
                  {t('panels.quickActions.submit')}
                </button>
              </div>
            </div>
          </div>
        ) : (
          <button
            onClick={() => { setAddingNew(true); setEditingId(null); }}
            className="flex items-center gap-1 w-full rounded-md border border-dashed border-border px-3 py-2 text-sm text-muted-foreground hover:border-info hover:text-info transition-colors"
          >
            <Plus className="h-4 w-4" />
            {t('panels.quickActions.addNew')}
          </button>
        )}
      </DialogContent>
    </Dialog>
  );
}
