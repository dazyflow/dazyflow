import { jsx as _jsx } from "react/jsx-runtime";
import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { App } from "./App";
import { AuthProvider } from "./auth";
// Side-effect import: initializes i18next + react-i18next before any
// component renders, so the first paint uses the user's locale. Must
// run before any useTranslation() call.
import "./i18n";
import "./theme.css";
import "./app.css";
import "@xyflow/react/dist/style.css";
ReactDOM.createRoot(document.getElementById("root")).render(_jsx(React.StrictMode, { children: _jsx(BrowserRouter, { children: _jsx(AuthProvider, { children: _jsx(App, {}) }) }) }));
