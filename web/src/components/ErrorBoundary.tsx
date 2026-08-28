// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The last line of defence. Neither SPA had one, so a render error anywhere in
// the tree unmounted it and left an empty document: no message, no reload, no
// indication the product had not simply died. React does this by design — an
// error during render is treated as unrecoverable and the whole tree goes —
// and only a boundary changes it.
//
// DELIBERATELY DEPENDENCY-FREE. No i18n, no router, no design-system button.
// This component runs precisely when something else has just failed, and every
// import it takes is another thing that can be the reason it cannot render.
// react-i18next in particular reads a module-global instance that a failed
// bootstrap may never have initialised, so a translated error page is one that
// disappears exactly when a bootstrap error is what you needed to see. Plain
// English and plain elements, styled with tokens the stylesheet already
// defines.
//
// Boundaries must be class components: there is no hook equivalent of
// getDerivedStateFromError.
import { Component, ErrorInfo, ReactNode } from "react";

type Props = {
  children: ReactNode;
  // Where "go back" leads. The app's root redirects a signed-in user onward;
  // the docs have no "/" route at all, so each entry names its own.
  home: string;
};

type State = { error: Error | null; stack: string };

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null, stack: "" };

  static getDerivedStateFromError(error: Error): Partial<State> {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // Kept in the console rather than posted anywhere: this build has no error
    // reporter, and inventing a network call on the failure path is how a
    // crash becomes a crash plus a hung request.
    console.error("Unhandled render error:", error, info.componentStack);
    this.setState({ stack: info.componentStack ?? "" });
  }

  render() {
    const { error, stack } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="crash-wrap">
        <div className="crash-card">
          <h1 className="crash-title">Something went wrong</h1>
          <p className="crash-body">
            This page hit an error and stopped. Reloading usually clears it — your
            work is saved on the server, not in this tab.
          </p>
          <div className="crash-actions">
            {/* A full reload, not a re-render: the tree that threw is the one
                still in memory, and re-rendering it walks into the same error. */}
            <button
              type="button"
              className="btn primary"
              onClick={() => window.location.reload()}
            >
              Reload the page
            </button>
            <a className="btn ghost" href={this.props.home}>
              Go back
            </a>
          </div>
          {/* Collapsed, because the message is for whoever the reader forwards
              it to, not for the reader. Open it and it is the one thing that
              makes a support report actionable. */}
          <details className="crash-details">
            <summary>Technical details</summary>
            <pre className="crash-trace">
              {error.message}
              {stack}
            </pre>
          </details>
        </div>
      </div>
    );
  }
}
