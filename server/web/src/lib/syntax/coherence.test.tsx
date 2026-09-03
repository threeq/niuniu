import { describe, it, expect } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { useSyntaxHighlight } from './use-syntax-highlight';
import { renderTokens } from './render-tokens';

/**
 * Regression: tokens must never be rendered against a different file's text.
 *
 * `useSyntaxHighlight` accumulates chunks into a ref and signals arrival with a
 * counter. When the `code` prop changes, the reset happens in an effect — which
 * runs *after* the render that already used the new code. If the returned
 * lookup is not tied to the text it was produced from, that render pairs the
 * previous file's tokens with the new file's lines. Because each token carries
 * its own `content`, the surface then displays the OLD file's text.
 */

function Probe({ code, path }: { code: string; path: string }) {
  const highlight = useSyntaxHighlight({ code, path });
  const lines = code.split('\n');
  return (
    <div data-testid="out">
      {lines.map((l, i) => (
        <div key={i}>{renderTokens(highlight(i), l)}</div>
      ))}
    </div>
  );
}

describe('token/text coherence', () => {
  it('never renders a previous file\'s text after the code prop changes', async () => {
    const fileA = 'const aaaaa = 1;';
    const fileB = 'const bbbbb = 2;';

    const { rerender } = render(<Probe code={fileA} path="a.ts" />);

    // Let A finish tokenizing, so the ref is fully populated.
    await waitFor(() => expect(screen.getByTestId('out').textContent).toContain('aaaaa'), {
      timeout: 15_000,
    });

    // Swap to a different file. The very next paint must already show B's text
    // — never A's, which is what stale tokens would render.
    rerender(<Probe code={fileB} path="a.ts" />);

    expect(screen.getByTestId('out').textContent).toContain('bbbbb');
    expect(screen.getByTestId('out').textContent).not.toContain('aaaaa');
  }, 25_000);
});
