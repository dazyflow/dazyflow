// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { AuthProvider } from "./auth";
import { ErrorBoundary } from "./components/ErrorBoundary";
// Initializes i18next + react-i18next before any component renders. i18nReady
// resolves once the resolved language's catalogue and drop vocabulary have
// been fetched — they are code-split per language, so the first paint waits on
// one of them rather than the entry chunk carrying all of them.
import { i18nReady } from "./i18n/index";
import { initTheme } from "./theme";
import "./theme.css";
import "./app.css";
import "@xyflow/react/dist/style.css";

// Apply the saved theme before first paint so there's no dark→light
// flash for users who picked light.
initTheme();

const root = ReactDOM.createRoot(document.getElementById("root")!);

// Rendering behind the language load, not in front of it: the strings are one
// fetch away and painting the app in the fallback language first would show a
// Swedish reader an English screen that redraws under them. A failed fetch
// still renders — untranslated beats blank.
void i18nReady
  .catch(() => {})
  .then(() =>
    root.render(
      <React.StrictMode>
        {/* Outside the router and the auth provider: a boundary inside them can
        only catch what they successfully rendered, and a failure in either is
        exactly the blank page this exists to prevent. */}
        <ErrorBoundary home="/">
          <BrowserRouter>
            <AuthProvider>
              <App />
            </AuthProvider>
          </BrowserRouter>
        </ErrorBoundary>
      </React.StrictMode>,
    ),
  );
