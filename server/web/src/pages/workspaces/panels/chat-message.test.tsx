import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ChatMessage, type TimelineEvent } from './chat-message';

const copyMock = vi.fn().mockResolvedValue(true);
vi.mock('@/lib/copy-to-clipboard', () => ({
  copyTextToClipboard: (text: string) => copyMock(text),
}));

const pinQueryMock = vi.fn().mockResolvedValue({ dashboard_id: 1, panel_id: 1 });
vi.mock('@/lib/dashboards-api', () => ({
  pinQuery: (body: unknown) => pinQueryMock(body),
}));
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const baseEvent = (content: string): TimelineEvent => ({
  id: 'e1',
  messageId: 'm1',
  type: 'text',
  role: 'assistant',
  content,
});

function renderMessage(content: string) {
  render(
    withQuery(
      <ChatMessage
        event={baseEvent(content)}
        cliType="claude"
        toolResults={new Map()}
        workspaceId="42"
      />,
    ),
  );
}

describe('ChatMessage niuniu-data rendering', () => {
  it('renders a DataResultBlock table header for a niuniu-data fence', () => {
    const block = {
      title: 'Top orders',
      result: {
        columns: [{ name: 'order_id', type: 'number' }],
        rows: [[1]],
        truncated: false,
        duration_ms: 3,
        engine: 'mysql',
      },
      chart: { type: 'table' },
      source: 'analytics',
      statement: 'SELECT order_id FROM orders',
    };
    const content = 'Here are the results:\n\n```niuniu-data\n' + JSON.stringify(block) + '\n```';
    renderMessage(content);
    // Table header column name from the parsed block.
    expect(screen.getByText('order_id')).toBeInTheDocument();
    // Surrounding prose still renders.
    expect(screen.getByText(/Here are the results/)).toBeInTheDocument();
  });

  it('treats a chart fence as an alias of niuniu-data', () => {
    const block = {
      result: {
        columns: [{ name: 'region', type: 'string' }],
        rows: [['east']],
        truncated: false,
        duration_ms: 1,
        engine: 'postgres',
      },
      chart: { type: 'table' },
    };
    const content = '```chart\n' + JSON.stringify(block) + '\n```';
    renderMessage(content);
    expect(screen.getByText('region')).toBeInTheDocument();
  });

  it('wraps a bare ECharts option from an echarts fence into a chart block', () => {
    const option = {
      xAxis: { type: 'category', data: ['a'] },
      yAxis: { type: 'value' },
      series: [{ type: 'bar', data: [1] }],
    };
    const content = 'chart below:\n\n```echarts\n' + JSON.stringify(option) + '\n```';
    renderMessage(content);
    // The bare option is wrapped into a DataBlock, so the fence no longer
    // renders as a raw code block (its JSON body is not visible as text).
    expect(screen.queryByText(/"xAxis"/)).not.toBeInTheDocument();
    expect(screen.getByText(/chart below/)).toBeInTheDocument();
  });

  it('falls back to a plain code block when the fence body is invalid JSON', () => {
    const content = 'broken:\n\n```niuniu-data\n{ not valid json }\n```';
    renderMessage(content);
    // The raw body survives as code text; no table header is produced.
    expect(screen.getByText(/not valid json/)).toBeInTheDocument();
  });

  it('renders plain assistant markdown unchanged', () => {
    renderMessage('just some **text**');
    expect(screen.getByText(/just some/)).toBeInTheDocument();
  });

  it('pins a static snapshot carrying the message creation time as queried_at', () => {
    // Regression for S1: a block WITH source+statement (the common case) must
    // still record the message time as the snapshot's data time, not fall back
    // to the pin time. The frontend pin lands as a static panel regardless.
    pinQueryMock.mockClear();
    const createdAt = 1718900000000;
    const block = {
      title: 'T',
      result: {
        columns: [{ name: 'id', type: 'number' }],
        rows: [[1]],
        truncated: false,
        duration_ms: 1,
        engine: 'mysql',
      },
      chart: { type: 'table' },
      source: 'analytics',
      statement: 'SELECT id FROM t',
    };
    const content = '```niuniu-data\n' + JSON.stringify(block) + '\n```';
    render(
      withQuery(
        <ChatMessage
          event={{ ...baseEvent(content), createdAt }}
          cliType="claude"
          toolResults={new Map()}
          workspaceId="42"
        />,
      ),
    );
    fireEvent.click(
      screen.getByRole('button', { name: /Pin 到数据看板|Pin to dashboard/ }),
    );
    expect(pinQueryMock).toHaveBeenCalledTimes(1);
    const body = pinQueryMock.mock.calls[0][0] as {
      snapshot?: { queried_at?: string };
    };
    expect(body.snapshot?.queried_at).toBe(new Date(createdAt).toISOString());
  });

  it('copies the block markdown to the clipboard when the copy button is clicked', () => {
    copyMock.mockClear();
    renderMessage('copy me **please**');
    fireEvent.click(screen.getByRole('button', { name: '复制消息' }));
    expect(copyMock).toHaveBeenCalledWith('copy me **please**');
  });
});
