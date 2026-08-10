import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { WorkspaceBgTasksRow } from './workspace-bg-tasks-row';
import type { BgTaskAggregateDTO } from '@/types/api';

describe('WorkspaceBgTasksRow', () => {
  it('renders nothing when bg_tasks is undefined', () => {
    const { container } = render(<WorkspaceBgTasksRow data={undefined} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders nothing when all counts zero and not busy', () => {
    const data: BgTaskAggregateDTO = {
      agent_busy: false, bash_count: 0, wakeup_count: 0,
      subagent_count: 0, cron_count: 0,
    };
    const { container } = render(<WorkspaceBgTasksRow data={data} />);
    expect(container.firstChild).toBeNull();
  });

  it('renders icon row when bash_count > 0', () => {
    const data: BgTaskAggregateDTO = {
      agent_busy: false, bash_count: 2, wakeup_count: 0,
      subagent_count: 0, cron_count: 0,
    };
    render(<WorkspaceBgTasksRow data={data} />);
    const bashIcon = screen.getByLabelText(/后台 bash/i);
    expect(bashIcon).toBeInTheDocument();
    expect(bashIcon.textContent).toMatch(/2/);
  });

  it('renders pulsing message icon when agent_busy', () => {
    const data: BgTaskAggregateDTO = {
      agent_busy: true, bash_count: 0, wakeup_count: 0,
      subagent_count: 0, cron_count: 0,
    };
    render(<WorkspaceBgTasksRow data={data} />);
    const el = screen.getByLabelText(/agent 在回复/i);
    expect(el.className).toMatch(/animate-pulse/);
  });

  it('renders bash highlight title with past-tense relative time', () => {
    // 8m + 30s slop so floor(elapsed/60) lands on 8 even with ~hundreds of
    // ms drift between the test set-up and component render.
    const startedMs = Date.now() - 8 * 60 * 1000 - 30 * 1000;
    const data: BgTaskAggregateDTO = {
      agent_busy: false, bash_count: 1, wakeup_count: 0,
      subagent_count: 0, cron_count: 0,
      highlight: {
        kind: 'bash',
        title: 'bash relay/deploy/deploy.sh',
        started_at: new Date(startedMs).toISOString(),
      },
    };
    render(<WorkspaceBgTasksRow data={data} />);
    const hl = screen.getByLabelText(/highlight/i);
    expect(hl.textContent).toMatch(/relay\/deploy\/deploy\.sh/);
    expect(hl.textContent).toMatch(/8m/);
  });

  it('renders wakeup highlight with future-tense relative time', () => {
    // 8m + 30s slop so floor(remaining/60) lands on 8 even with ~hundreds of
    // ms drift between the test set-up and component render.
    const scheduledMs = Date.now() + 8 * 60 * 1000 + 30 * 1000;
    const data: BgTaskAggregateDTO = {
      agent_busy: false, bash_count: 0, wakeup_count: 1,
      subagent_count: 0, cron_count: 0,
      highlight: {
        kind: 'wakeup',
        title: 'check build progress',
        scheduled_for: new Date(scheduledMs).toISOString(),
      },
    };
    render(<WorkspaceBgTasksRow data={data} />);
    const hl = screen.getByLabelText(/highlight/i);
    expect(hl.textContent).toMatch(/check build progress/);
    expect(hl.textContent).toMatch(/in 8m/);
  });
});
