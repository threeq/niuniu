import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { PermissionPromptCard } from './PermissionPromptCard';
import type { PermissionRequest } from '@/types/permission';

const baseRequest: PermissionRequest = {
  id: 1,
  workspace_id: 1,
  session_id: 's',
  tool_name: 'Bash',
  tool_input: { command: 'npm test' },
  requested_at: Date.now(),
  // year ~2099 — well past any test run
  expires_at: Date.now() + 1000 * 60 * 60 * 24 * 365 * 73,
  status: 'pending',
};

describe('PermissionPromptCard', () => {
  it('renders tool name and tool input for a pending request', () => {
    render(<PermissionPromptCard request={baseRequest} onDecide={vi.fn()} />);
    // Bash tool name appears somewhere in the title
    expect(screen.getByText(/Bash/)).toBeInTheDocument();
    // The Bash command appears via PermissionInputRenderer
    expect(screen.getByText(/npm test/)).toBeInTheDocument();
    // At minimum: allow / always-allow / deny buttons
    expect(screen.getAllByRole('button').length).toBeGreaterThanOrEqual(3);
  });

  it('calls onDecide(allow,once) when allow button clicked', () => {
    const onDecide = vi.fn();
    render(<PermissionPromptCard request={baseRequest} onDecide={onDecide} />);
    // Locale-agnostic: the first decide button is "allow once"
    const buttons = screen.getAllByRole('button');
    fireEvent.click(buttons[0]);
    expect(onDecide).toHaveBeenCalledWith({ decision: 'allow', scope: 'once' });
  });

  it('renders collapsed status badge when status != pending (no decide buttons)', () => {
    const allowed: PermissionRequest = { ...baseRequest, status: 'allowed' };
    render(<PermissionPromptCard request={allowed} onDecide={vi.fn()} />);
    // Collapsed state has no buttons (no allow/deny UX once decided)
    expect(screen.queryAllByRole('button')).toHaveLength(0);
    // Tool name still surfaces in the badge for context
    expect(screen.getByText(/Bash/)).toBeInTheDocument();
  });

  it('low-risk tool (Read) sends always-allow with kind=any matcher', () => {
    const onDecide = vi.fn();
    const readReq: PermissionRequest = {
      ...baseRequest,
      tool_name: 'Read',
      tool_input: { file_path: '/tmp/foo.txt' },
    };
    render(<PermissionPromptCard request={readReq} onDecide={onDecide} />);
    // Buttons: [allow once, always allow, deny]
    const buttons = screen.getAllByRole('button');
    fireEvent.click(buttons[1]);
    expect(onDecide).toHaveBeenCalledWith({
      decision: 'allow',
      scope: 'always',
      matcher: { kind: 'any', value: '' },
    });
  });

  it('clicking deny reveals the reason textarea + confirm/cancel buttons', () => {
    const onDecide = vi.fn();
    render(<PermissionPromptCard request={baseRequest} onDecide={onDecide} />);
    const buttons = screen.getAllByRole('button');
    // Last button in the row is the "deny" trigger
    const denyTrigger = buttons[buttons.length - 1];
    fireEvent.click(denyTrigger);
    // Textarea is present after expansion
    const textbox = screen.getByRole('textbox');
    expect(textbox).toBeInTheDocument();
    fireEvent.change(textbox, { target: { value: 'too risky' } });
    // After expansion, two buttons remain: confirm + cancel
    const expandedButtons = screen.getAllByRole('button');
    fireEvent.click(expandedButtons[0]);
    expect(onDecide).toHaveBeenCalledWith({
      decision: 'deny',
      scope: 'once',
      deny_message: 'too risky',
    });
  });
});
