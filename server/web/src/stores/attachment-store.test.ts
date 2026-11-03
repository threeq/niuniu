import { beforeEach, describe, expect, it } from 'vitest';
import {
  useAttachmentStore,
  selectWorkspaceAttachments,
  selectWorkspacePendingAttachments,
} from './attachment-store';

const att = (path: string) => ({ path, type: 'upload' as const, name: path });

beforeEach(() => {
  useAttachmentStore.setState({
    attachmentsByWorkspace: {},
    pendingByWorkspace: {},
    uploading: false,
  });
});

describe('attachment store — per-workspace isolation', () => {
  it('keeps staged attachments separate between workspaces', () => {
    const s = useAttachmentStore.getState();
    s.addAttachment('ws-a', att('.attachments/a.docx'));

    // Workspace B has nothing staged, even though A does.
    expect(selectWorkspaceAttachments('ws-b')(useAttachmentStore.getState())).toHaveLength(0);
    expect(selectWorkspaceAttachments('ws-a')(useAttachmentStore.getState())).toHaveLength(1);

    s.addAttachment('ws-b', att('.attachments/b.pdf'));
    const after = useAttachmentStore.getState();
    expect(selectWorkspaceAttachments('ws-a')(after).map((a) => a.path)).toEqual([
      '.attachments/a.docx',
    ]);
    expect(selectWorkspaceAttachments('ws-b')(after).map((a) => a.path)).toEqual([
      '.attachments/b.pdf',
    ]);
  });

  it('clearing one workspace leaves the other untouched', () => {
    const s = useAttachmentStore.getState();
    s.addAttachment('ws-a', att('.attachments/a.docx'));
    s.addAttachment('ws-b', att('.attachments/b.pdf'));

    useAttachmentStore.getState().clearAttachments('ws-a');
    const after = useAttachmentStore.getState();
    expect(selectWorkspaceAttachments('ws-a')(after)).toHaveLength(0);
    expect(selectWorkspaceAttachments('ws-b')(after)).toHaveLength(1);
  });

  it('tracks in-flight uploads per workspace', () => {
    const s = useAttachmentStore.getState();
    s.addPendingAttachment('ws-a', 'big.zip', 1024);

    expect(selectWorkspacePendingAttachments('ws-b')(useAttachmentStore.getState())).toHaveLength(0);
    expect(selectWorkspacePendingAttachments('ws-a')(useAttachmentStore.getState())).toHaveLength(1);

    // Moving the upload into place only affects its own workspace.
    s.movePendingToAttachment('ws-a', 'big.zip', att('.attachments/big.zip'));
    const after = useAttachmentStore.getState();
    expect(selectWorkspacePendingAttachments('ws-a')(after)).toHaveLength(0);
    expect(selectWorkspaceAttachments('ws-a')(after)).toHaveLength(1);
    expect(selectWorkspaceAttachments('ws-b')(after)).toHaveLength(0);
  });

  it('returns a stable empty array for workspaces with nothing staged', () => {
    const first = selectWorkspaceAttachments('empty')(useAttachmentStore.getState());
    const second = selectWorkspaceAttachments('empty')(useAttachmentStore.getState());
    // Identity matters: a fresh [] per snapshot would re-render forever.
    expect(first).toBe(second);
  });
});
