import { Component, type ErrorInfo, type ReactNode } from "react";

interface ErrorBoundaryProps {
  children: ReactNode;
  /** Optional label for the area that failed (e.g. the page name). */
  label?: string;
}

interface ErrorBoundaryState {
  error: Error | null;
  info: ErrorInfo | null;
}

/**
 * Catches render-time errors in its subtree and shows a readable message
 * instead of a blank white screen (React unmounts the whole tree on an
 * uncaught render throw). Keep this wrapping each routed page so one bad
 * API payload can't take down the entire admin app.
 */
export default class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  state: ErrorBoundaryState = { error: null, info: null };

  static getDerivedStateFromError(error: Error): Partial<ErrorBoundaryState> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    this.setState({ error, info });
    console.error("Admin render error", error, info);
  }

  private reset = () => this.setState({ error: null, info: null });

  render() {
    const { error, info } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="space-y-4">
        <div role="alert" className="alert alert-error">
          <div>
            <h2 className="font-semibold">
              {this.props.label ? `${this.props.label} failed to render` : "Something went wrong"}
            </h2>
            <p className="text-sm opacity-90">{error.message || String(error)}</p>
          </div>
          <div className="flex gap-2">
            <button type="button" className="btn btn-sm" onClick={this.reset}>
              Try again
            </button>
            <button
              type="button"
              className="btn btn-sm btn-outline"
              onClick={() => window.location.reload()}
            >
              Reload
            </button>
          </div>
        </div>
        {error.stack ? (
          <details className="rounded-lg bg-base-200 p-4 text-xs">
            <summary className="cursor-pointer font-medium">Error details</summary>
            <pre className="mt-2 overflow-x-auto whitespace-pre-wrap break-words text-base-content/70">
              {error.stack}
              {info?.componentStack ? `\n\nComponent stack:${info.componentStack}` : ""}
            </pre>
          </details>
        ) : null}
      </div>
    );
  }
}
