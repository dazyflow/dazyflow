// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { AuthProvider } from "./auth";
import { ErrorBoundary } from "./components/ErrorBoundary";
// Side-effect import: initializes i18next + react-i18next before any
// component renders, so the first paint uses the user's locale. Must
// run before any useTranslation() call.
import "./i18n/index";
import { initTheme } from "./theme";
import "./theme.css";
import "./app.css";
import "@xyflow/react/dist/style.css";

// Apply the saved theme before first paint so there's no dark→light
// flash for users who picked light.
initTheme();

ReactDOM.createRoot(document.getElementById("root")!).render(
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
);
