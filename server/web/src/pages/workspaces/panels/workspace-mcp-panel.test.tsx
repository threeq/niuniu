import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { it, expect, vi, beforeEach, describe } from 'vitest';
import { WorkspaceMCPPanel } from './workspace-mcp-panel';
import { mcpApi } from '@/lib/api';

// We mock the mcpApi namespace surface used by the panel. The test setup
// (src/i18n/test-setup.ts) forces zh-CN, so assertions match the zh-CN
// strings registered in workspaces.json under the `mcp` key.
vi.mock('@/lib/api', () => ({
  mcpApi: {
    get: vi.fn(),
    put: vi.fn(),
    redetect: vi.fn(),
  },
  api: {
    stopAgent: vi.fn(),
  },
}));

const mockedGet = vi.mocked(mcpApi.get);
const mockedPut = vi.mocked(mcpApi.put);

beforeEach(() => {
  vi.clearAllMocks();
});

describe('WorkspaceMCPPanel', () => {
  it('renders the available server, the niuniu required row, and an unavailable warning', async () => {
    mockedGet.mockResolvedValue({
      servers: ['playwright'],
      unavailable: ['ghost-mcp'],
      available: [
        {
          name: 'playwright',
          command: 'npx',
          args: ['-y', '@playwright/mcp'],
          source: 'global',
        },
      ],
      plugin_conflicts: [],
    } as never);

    render(<WorkspaceMCPPanel workspaceId={42} />);

    // The niuniu row is always present and labelled "必选" (Required, zh-CN).
    await waitFor(() => {
      expect(screen.getByTestId('mcp-row-niuniu')).toBeInTheDocument();
    });
    expect(screen.getByText('必选')).toBeInTheDocument();

    // The detected MCP renders as a toggleable row.
    expect(screen.getByTestId('mcp-row-playwright')).toBeInTheDocument();

    // Unavailable warning ("1 个 MCP 在当前账号下不可用") + the unavailable
    // name surface together.
    expect(screen.getByText(/MCP 在当前账号下不可用/)).toBeInTheDocument();
    expect(screen.getByText(/ghost-mcp/)).toBeInTheDocument();
  });

  it('marks the form dirty when a checkbox toggles and enables Save', async () => {
    mockedGet.mockResolvedValue({
      servers: ['playwright'],
      unavailable: [],
      available: [
        {
          name: 'playwright',
          command: 'npx',
          args: ['-y', '@playwright/mcp'],
          source: 'global',
        },
      ],
      plugin_conflicts: [],
    } as never);
    mockedPut.mockResolvedValue({
      written: true,
      written_servers: [],
      unavailable: [],
    } as never);

    render(<WorkspaceMCPPanel workspaceId={42} />);

    // Save button starts disabled because the local selection matches what
    // was loaded from the server (no dirty state yet).
    const saveBtn = await waitFor(() =>
      screen.getByTestId('mcp-save') as HTMLButtonElement
    );
    expect(saveBtn.disabled).toBe(true);

    // Toggling the playwright row marks the form dirty -> Save becomes
    // enabled. The Checkbox primitive renders the actual toggle inside the
    // <label> row, so a click on the row fires onCheckedChange.
    const row = screen.getByTestId('mcp-row-playwright');
    fireEvent.click(row.querySelector('button[role="checkbox"]')!);

    await waitFor(() => {
      expect((screen.getByTestId('mcp-save') as HTMLButtonElement).disabled).toBe(false);
    });

    // Clicking Save flushes the selection to mcpApi.put with workspaceId=42
    // and the un-checked playwright list.
    fireEvent.click(screen.getByTestId('mcp-save'));
    await waitFor(() => {
      expect(mockedPut).toHaveBeenCalledWith(42, []);
    });
  });
});
