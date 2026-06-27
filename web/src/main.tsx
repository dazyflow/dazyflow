// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { AuthProvider } from "./auth";
// Side-effect import: initializes i18next + react-i18next before any
// component renders, so the first paint uses the user's locale. Must
// run before any useTranslation() call.
import "./i18n";
import { initTheme } from "./theme";
import "./theme.css";
import "./app.css";
import "@xyflow/react/dist/style.css";

// Apply the saved theme before first paint so there's no dark→light
// flash for users who picked light.
initTheme();

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter>
      <AuthProvider>
        <App />
      </AuthProvider>
    </BrowserRouter>
  </React.StrictMode>,
);
