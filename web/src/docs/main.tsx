// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Entry for the public docs SPA (docs.dazyflow.app). It deliberately reuses the
// app's design system — theme.css + app.css — and the shell markup (see
// DocsShell), so the docs are the same GUI as the product. No AuthProvider / no
// i18n: the docs are public and English-only.
import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { DocsApp } from "./DocsApp";
import "../theme.css";
import "../app.css";
import "./docs.css";

// Docs render dark, matching the marketing site + the editor's default look.
document.documentElement.setAttribute("data-theme", "dark");

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <DocsApp />
    </BrowserRouter>
  </React.StrictMode>,
);
