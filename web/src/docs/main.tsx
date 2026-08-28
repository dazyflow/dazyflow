// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Entry for the public docs SPA (docs.dazyflow.app). It deliberately reuses the
// app's design system — theme.css + app.css — and the shell markup (see
// DocsShell), so the docs are the same GUI as the product. No AuthProvider / no
// i18n: the docs are public and English-only.
import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { DocsApp } from "./DocsApp";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { DOCS_HOME } from "./links";
import "../theme.css";
import "../app.css";
import "./docs.css";

// Docs render light. A reference page is read at length rather than glanced
// at, and light is what most readers' machines are already in — the same
// reasoning theme.ts gives for the app defaulting to the OS rather than to
// dark. Pinned rather than following prefers-color-scheme because the docs
// carry no theme control of their own: a reader who landed in the wrong one
// would have no way to change it.
document.documentElement.setAttribute("data-theme", "light");

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <ErrorBoundary home={DOCS_HOME}>
      <BrowserRouter>
        <DocsApp />
      </BrowserRouter>
    </ErrorBoundary>
  </React.StrictMode>,
);
