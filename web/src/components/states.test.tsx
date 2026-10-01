import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ErrorBoundary } from './ErrorBoundary';
import { StateView, stillLoading, type StateKind } from './states';
import { ApiClientError } from '../api/client';

const KINDS: StateKind[] = ['loading', 'empty', 'error', 'permission', 'conflict', 'stale', 'tenant-switch', 'rollback'];

describe('StateView', () => {
  it('renders a labelled state for every supported kind', () => {
    for (const kind of KINDS) {
      const { unmount } = render(<StateView kind={kind} />);
      expect(screen.getByRole(kind === 'error' ? 'alert' : 'status')).toBeInTheDocument();
      unmount();
    }
  });

  it('exposes a retry control only for retryable errors', async () => {
    const onRetry = vi.fn();
    const { rerender } = render(<StateView kind="error" error={new ApiClientError('boom', { code: 'DB_UNAVAILABLE', status: 503, retryable: true })} onRetry={onRetry} />);
    expect(screen.getByTestId('error-code').textContent).toBe('DB_UNAVAILABLE');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalledTimes(1);

    rerender(<StateView kind="error" error={new ApiClientError('nope', { code: 'FORBIDDEN', status: 403, retryable: false })} onRetry={onRetry} />);
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument();
  });

  it('identifies loading and tenant-switch as still-loading', () => {
    expect(stillLoading('loading')).toBe(true);
    expect(stillLoading('tenant-switch')).toBe(true);
    expect(stillLoading('ready' as StateKind)).toBe(false);
  });
});

describe('ErrorBoundary', () => {
  function Boom(): never {
    throw new Error('render exploded');
  }

  it('renders a fallback and can reset', async () => {
    const onError = vi.fn();
    render(
      <ErrorBoundary onError={onError} fallback={(error, reset) => (
        <div>
          <span>{error.message}</span>
          <button onClick={reset}>reset</button>
        </div>
      )}>
        <Boom />
      </ErrorBoundary>,
    );
    expect(screen.getByText('render exploded')).toBeInTheDocument();
    expect(onError).toHaveBeenCalled();
  });
});