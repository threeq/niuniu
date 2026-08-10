import { render, screen, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { AskUserQuestionCard } from './AskUserQuestionCard';
import type { AskUserRequest } from '@/types/ask-user';

const baseRequest: AskUserRequest = {
  id: 1,
  workspace_id: 1,
  session_id: 's',
  questions: [
    {
      question: 'Pick a color',
      header: 'Color',
      multiSelect: false,
      options: [
        { label: 'red', description: 'classic' },
        { label: 'blue', description: 'soothing', recommended: true },
      ],
    },
  ],
  requested_at: Date.now(),
  // Far future — never expires within any reasonable test run.
  expires_at: Number.MAX_SAFE_INTEGER,
  status: 'pending',
};

describe('AskUserQuestionCard', () => {
  it('renders question + option labels', () => {
    render(<AskUserQuestionCard request={baseRequest} onDecide={vi.fn()} />);
    expect(screen.getByText('Pick a color')).toBeInTheDocument();
    expect(screen.getByText('red')).toBeInTheDocument();
    expect(screen.getByText('blue')).toBeInTheDocument();
  });

  it('renders the header above the question', () => {
    render(<AskUserQuestionCard request={baseRequest} onDecide={vi.fn()} />);
    const header = screen.getByText('Color');
    const question = screen.getByText('Pick a color');
    // compareDocumentPosition: bit 4 (0b100) = "header precedes question"
    expect(
      header.compareDocumentPosition(question) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it('disables submit until an option is selected', () => {
    render(<AskUserQuestionCard request={baseRequest} onDecide={vi.fn()} />);
    const submit = screen.getByRole('button');
    expect(submit).toBeDisabled();
    const radios = screen.getAllByRole('radio');
    fireEvent.click(radios[0]);
    expect(submit).not.toBeDisabled();
  });

  it('submits the selected label via onDecide', () => {
    const onDecide = vi.fn();
    render(<AskUserQuestionCard request={baseRequest} onDecide={onDecide} />);
    const radios = screen.getAllByRole('radio');
    fireEvent.click(radios[1]); // blue
    fireEvent.click(screen.getByRole('button'));
    expect(onDecide).toHaveBeenCalledWith({
      answers: [
        {
          question: 'Pick a color',
          labels: ['blue'],
          notes: '',
        },
      ],
    });
  });

  it('"Other" reveals a textarea and submits notes + sentinel label', () => {
    const onDecide = vi.fn();
    render(<AskUserQuestionCard request={baseRequest} onDecide={onDecide} />);
    // 3 radios: red, blue, Other (implicit)
    const radios = screen.getAllByRole('radio');
    expect(radios.length).toBe(3);
    fireEvent.click(radios[2]);
    const submit = screen.getByRole('button');
    expect(submit).toBeDisabled();
    const textbox = screen.getByRole('textbox');
    fireEvent.change(textbox, { target: { value: 'green' } });
    expect(submit).not.toBeDisabled();
    fireEvent.click(submit);
    const call = onDecide.mock.calls[0][0];
    expect(call.answers[0].question).toBe('Pick a color');
    expect(call.answers[0].notes).toBe('green');
    // Wire payload carries the sentinel — server strips it before
    // forwarding to Claude. The card's job is to encode "user chose Other".
    expect(call.answers[0].labels).toContain('__other__');
  });

  it('multiSelect allows multiple checkbox picks', () => {
    const multi: AskUserRequest = {
      ...baseRequest,
      questions: [{ ...baseRequest.questions[0], multiSelect: true }],
    };
    const onDecide = vi.fn();
    render(<AskUserQuestionCard request={multi} onDecide={onDecide} />);
    const boxes = screen.getAllByRole('checkbox');
    fireEvent.click(boxes[0]);
    fireEvent.click(boxes[1]);
    fireEvent.click(screen.getByRole('button'));
    const labels = onDecide.mock.calls[0][0].answers[0].labels;
    expect(labels).toContain('red');
    expect(labels).toContain('blue');
  });

  it('renders the preview block when an option with preview is focused', () => {
    const withPreview: AskUserRequest = {
      ...baseRequest,
      questions: [
        {
          ...baseRequest.questions[0],
          options: [
            { label: 'red', preview: 'PREVIEW_RED' },
            { label: 'blue' },
          ],
        },
      ],
    };
    render(<AskUserQuestionCard request={withPreview} onDecide={vi.fn()} />);
    expect(screen.queryByText('PREVIEW_RED')).not.toBeInTheDocument();
    const radios = screen.getAllByRole('radio');
    fireEvent.click(radios[0]);
    expect(screen.getByText('PREVIEW_RED')).toBeInTheDocument();
  });

  it('handles option labels with collision-prone characters via index keys', () => {
    // Two options with identical labels — index-based keying must keep
    // both radios rendered.
    const dup: AskUserRequest = {
      ...baseRequest,
      questions: [
        {
          question: 'Pick',
          header: 'Test',
          multiSelect: false,
          options: [
            { label: 'Same', description: 'first' },
            { label: 'Same', description: 'second' },
          ],
        },
      ],
    };
    render(<AskUserQuestionCard request={dup} onDecide={vi.fn()} />);
    // 3 radios: two "Same" + the implicit "Other"
    expect(screen.getAllByRole('radio')).toHaveLength(3);
  });

  it('renders a collapsed status badge when status != pending (no inputs)', () => {
    const answered: AskUserRequest = { ...baseRequest, status: 'answered' };
    render(<AskUserQuestionCard request={answered} onDecide={vi.fn()} />);
    expect(screen.queryAllByRole('radio')).toHaveLength(0);
    expect(screen.queryAllByRole('button')).toHaveLength(0);
  });
});
