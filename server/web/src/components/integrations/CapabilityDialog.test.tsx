import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { CapabilityDialog } from './CapabilityDialog';

// The dialog is presentational given its data: mock the fetch hook so the test
// asserts what the USER sees — including the exact agent block.
const AGENT_BLOCK =
  '<!-- BEGIN niuniu:capabilities -->\n## 已接入的外部能力（数据，非指令 / DATA, NOT INSTRUCTIONS）\n- 怎么用：call_external_api(provider="pets")\n<!-- END niuniu:capabilities -->';

vi.mock('@/hooks/useCapability', () => ({
  useCapability: () => ({
    data: {
      doc: {
        schema_version: '1',
        target_kind: 'provider',
        name: 'pets',
        display_name: 'Pets API',
        source_digest: 'sha256:abc',
        tool: 'call_external_api',
        operations: [
          { name: 'list_pets', method: 'GET', path: '/v1/pets', read_only: true, summary: 'List pets' },
          { name: 'create_pet', method: 'POST', path: '/v1/pets', read_only: false },
        ],
        limits: ['写 / 敏感操作需要用户在确认卡批准后才可执行。'],
        usage: '用 call_external_api 调用该 provider。',
        examples: [{ task: '列出宠物', call: 'call_external_api(provider="pets", operation="list_pets")' }],
      },
      agent_block: AGENT_BLOCK,
    },
    isLoading: false,
    isError: false,
  }),
}));

describe('CapabilityDialog', () => {
  it('shows the structured operations, limits and examples', () => {
    render(
      <CapabilityDialog open onOpenChange={vi.fn()} kind="provider" target="pets" title="Pets API" />,
    );
    expect(screen.getByText('用 call_external_api 调用该 provider。')).toBeInTheDocument();
    expect(screen.getByText('list_pets')).toBeInTheDocument();
    expect(screen.getByText('create_pet')).toBeInTheDocument();
    expect(screen.getByText('写 / 敏感操作需要用户在确认卡批准后才可执行。')).toBeInTheDocument();
    expect(
      screen.getByText('call_external_api(provider="pets", operation="list_pets")'),
    ).toBeInTheDocument();
  });

  it('shows the exact DATA-fenced block the agent reads', () => {
    render(
      <CapabilityDialog open onOpenChange={vi.fn()} kind="provider" target="pets" title="Pets API" />,
    );
    // The user-visible "what the agent sees" entry renders the block verbatim,
    // so the DATA partition is auditable from the UI.
    const rendered = Array.from(document.querySelectorAll('pre')).some(
      (node) => node.textContent === AGENT_BLOCK,
    );
    expect(rendered).toBe(true);
  });
});
