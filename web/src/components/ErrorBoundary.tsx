import { Component, type ErrorInfo, type ReactNode } from 'react';
import { useI18n } from '../i18n';

export interface ErrorBoundaryProps {
  children: ReactNode;
  fallback?: (error: Error, reset: () => void) => ReactNode;
  onError?: (error: Error, info: ErrorInfo) => void;
}

interface ErrorBoundaryState {
  error?: Error;
}

// ErrorFallback is a functional component so it can read the active language.
function ErrorFallback({ error, onRetry }: { error: Error; onRetry: () => void }) {
  const { t } = useI18n();
  return (
    <section role="alert" className="state state--error">
      <h2>{t('error.title')}</h2>
      <p>{error.message}</p>
      <button onClick={onRetry}>{t('error.retry')}</button>
    </section>
  );
}

// ErrorBoundary turns an unexpected render failure into an explicit error state
// so a single broken panel cannot blank the whole console.
export class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = {};

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    this.props.onError?.(error, info);
  }

  reset = (): void => {
    this.setState({ error: undefined });
  };

  render(): ReactNode {
    const { error } = this.state;
    if (error) {
      if (this.props.fallback) {
        return this.props.fallback(error, this.reset);
      }
      return <ErrorFallback error={error} onRetry={this.reset} />;
    }
    return this.props.children;
  }
}
