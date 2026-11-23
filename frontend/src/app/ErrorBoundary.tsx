import { Component, type ErrorInfo, type ReactNode } from "react";

import { logger } from "./logging";

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * Catches render errors so a single bug does not leave a blank white page.
 *
 * React 19 unmounts the whole tree when a render throws; without this boundary
 * the only trace is a console entry nobody sees during a demo.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    logger.error("render error", error, info.componentStack);
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        <div className="app-error" role="alert">
          <h1>Terjadi kesalahan pada antarmuka</h1>
          <p>Muat ulang halaman. Jika masalah berlanjut, laporkan pesan di bawah.</p>
          <pre>{this.state.error.message}</pre>
          <button type="button" onClick={() => window.location.reload()}>
            Muat ulang
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}
