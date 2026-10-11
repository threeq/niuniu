import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { it, expect, vi, beforeEach } from 'vitest';
import { IntegrationDraftPanel } from './IntegrationDraftPanel';
import * as draftApi from '@/lib/integration-draft-api';
import type { IntegrationDraftView } from '@/types/integration-draft';

vi.mock('@/lib/integration-draft-api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/integration-draft-api')>();
  return {
    ...actual,
    integrationDraftApi: {
      list: vi.fn(),
      get: vi.fn(),
      confirm: vi.fn(),
      publish: vi.fn(),
      remove: vi.fn(),
      versions: vi.fn().mockResolvedValue([]),
      rollback: vi.fn(),
    },
  };
});

const mocked = vi.mocked(draftApi.integrationDraftApi);

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

function draft(overrides: Partial<IntegrationDraftView> = {}): IntegrationDraftView {
  return {
    id: 7,
    kind: 'provider',
    target_name: 'acme-crm',
    target_id: 0,
    status: 'draft',
    agent_authored: true,
    issues: [],
    created_at: '2026-10-11T00:00:00Z',
    card: {
      kind: 'provider',
      title: 'acme-crm',
      origin: { kind: 'openapi', ref: 'https://acme.example/openapi.json' },
      operations: [
        { name: 'listContacts', method: 'GET', path: '/contacts', read_only: true, high_risk: false },
      ],
      verify_probe: {
        name: 'listContacts',
        method: 'GET',
        path: '/contacts',
        read_only: true,
        high_risk: false,
      },
      scopes: [],
      diff: [],
      risks: [],
    },
    ...overrides,
  };
}

beforeEach(() => vi.clearAllMocks());

it('renders the system card and labels the action as confirming the impact, not publishing', async () => {
  mocked.list.mockResolvedValue([draft()]);
  wrap(<IntegrationDraftPanel />);

  // Select the draft.
  fireEvent.click(await screen.findByText('acme-crm'));

  // The decision area comes from the server model, verbatim.
  expect(await screen.findByTestId('integration-confirm-card')).toBeInTheDocument();
  // The operation row and the verify-probe row both render it.
  expect(screen.getAllByText('/contacts').length).toBeGreaterThan(0);
  // Confirm and publish are two steps in this pipeline, so the button must say
  // what it does.
  expect(screen.getByRole('button', { name: '确认影响范围' })).toBeInTheDocument();
  expect(screen.queryByRole('button', { name: '发布' })).not.toBeInTheDocument();
});

it('gates a widening draft behind a separate acknowledgement before the card can be approved', async () => {
  mocked.list.mockResolvedValue([
    draft({
      requires_second_confirmation: true,
      pending_risk_codes: ['scope_widened'],
      card: {
        ...draft().card!,
        risks: [
          {
            code: 'scope_widened',
            severity: 'high',
            target: 'tables_allow',
            detail: '/v1/admin',
            ack_key: 'scope_widened:tables_allow',
          },
        ],
      },
    }),
  ]);
  wrap(<IntegrationDraftPanel />);
  fireEvent.click(await screen.findByText('acme-crm'));

  const confirmButton = await screen.findByRole('button', { name: '确认影响范围' });
  // The second confirmation is a distinct control rendered before the card;
  // DOM order gives [widening ack, card risk ack].
  const boxes = screen.getAllByRole('checkbox');
  expect(boxes).toHaveLength(2);
  const [wideningAck, riskAck] = boxes;

  // The separate widening acknowledgement is a PREREQUISITE: until it is given,
  // even the card's own risk items cannot be ticked and approve stays disabled.
  expect(confirmButton).toBeDisabled();
  expect(riskAck).toBeDisabled();

  fireEvent.click(wideningAck);
  await waitFor(() => expect(riskAck).toBeEnabled());
  expect(confirmButton).toBeDisabled();

  // Only after both does the approval become possible.
  fireEvent.click(riskAck);
  await waitFor(() => expect(confirmButton).toBeEnabled());
});

it('offers Publish only once the draft is confirmed', async () => {
  mocked.list.mockResolvedValue([draft({ status: 'confirmed' })]);
  wrap(<IntegrationDraftPanel />);
  fireEvent.click(await screen.findByText('acme-crm'));

  expect(await screen.findByRole('button', { name: '发布' })).toBeInTheDocument();
});

it('shows the returned card for a scope-changing rollback instead of claiming it applied', async () => {
  mocked.list.mockResolvedValue([draft({ status: 'published' })]);
  mocked.versions.mockResolvedValue([
    {
      id: 3,
      kind: 'provider',
      target_name: 'acme-crm',
      target_id: 1,
      version: 1,
      active: 0,
      published_by: 1,
      created_at: '2026-10-10T00:00:00Z',
    },
  ]);
  mocked.rollback.mockResolvedValue({
    rolled_back: false,
    enabled: true,
    requires_confirmation: true,
    card: {
      kind: 'provider',
      title: 'acme-crm',
      origin: { kind: 'user_note' },
      operations: [],
      scopes: [],
      diff: [{ field: 'operation', after: 'POST /contacts', change: 'added' }],
      risks: [],
    },
  });

  wrap(<IntegrationDraftPanel />);
  fireEvent.click(await screen.findByText('acme-crm'));
  fireEvent.click(await screen.findByRole('button', { name: '回退' }));

  await waitFor(() => expect(mocked.rollback).toHaveBeenCalledWith(3, { confirmed: false, acknowledged: [] }));
  // Nothing was applied, and the user gets a fresh card to decide on.
  expect(screen.queryByText('已回退。')).not.toBeInTheDocument();
  expect(await screen.findByRole('button', { name: '确认并回退' })).toBeInTheDocument();
  expect(screen.getAllByTestId('integration-confirm-card').length).toBeGreaterThan(1);
});
