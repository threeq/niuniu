import React from 'react';
import { render, fireEvent } from '@testing-library/react-native';
import { CodeInput } from '../CodeInput';

describe('CodeInput', () => {
  it('renders 6 input cells', () => {
    const { getAllByTestId } = render(
      <CodeInput value="" onChange={jest.fn()} onComplete={jest.fn()} />,
    );
    // Use testID, not accessibilityRole — RN TextInput's default role is none/textbox,
    // and the value 'text' isn't a recognized A11yRole on TextInput.
    const cells = getAllByTestId(/^code-cell-\d$/);
    expect(cells).toHaveLength(6);
  });

  it('calls onChange as digits typed', () => {
    const onChange = jest.fn();
    const { getAllByTestId } = render(
      <CodeInput value="" onChange={onChange} onComplete={jest.fn()} />,
    );
    const cells = getAllByTestId(/code-cell-/);
    fireEvent.changeText(cells[0], '1');
    expect(onChange).toHaveBeenLastCalledWith('1');
  });

  it('fires onComplete when 6 digits filled', () => {
    const onComplete = jest.fn();
    const { rerender } = render(
      <CodeInput value="" onChange={jest.fn()} onComplete={onComplete} />,
    );
    rerender(<CodeInput value="123456" onChange={jest.fn()} onComplete={onComplete} />);
    // Wait for the 200ms debounce.
    return new Promise((r) =>
      setTimeout(() => {
        expect(onComplete).toHaveBeenCalledWith('123456');
        r(undefined);
      }, 250),
    );
  });

  it('handles paste of 6-digit string into first cell', () => {
    const onChange = jest.fn();
    const { getAllByTestId } = render(
      <CodeInput value="" onChange={onChange} onComplete={jest.fn()} />,
    );
    const cells = getAllByTestId(/code-cell-/);
    fireEvent.changeText(cells[0], '987654');
    expect(onChange).toHaveBeenLastCalledWith('987654');
  });
});
