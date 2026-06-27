// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Theme mode: dark (default, the synthwave editor look) or light (the
// designed-fresh professional palette). Persisted per browser in
// localStorage and applied as data-theme on <html>, which every CSS
// token keys off. No server-side user setting yet — browser-local only.
import { useEffect, useState } from "react";

export type ThemeMode = "dark" | "light";

const KEY = "dazyflow.theme";

export function getTheme(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark") return v;
  } catch {
    /* storage blocked — fall through to default */
  }
  return "dark";
}

export function applyTheme(mode: ThemeMode): void {
  document.documentElement.setAttribute("data-theme", mode);
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* non-essential — the attribute is already applied for this session */
  }
}

// initTheme runs once at boot (before React renders) so the first paint
// matches the saved choice rather than the data-theme baked into
// index.html.
export function initTheme(): void {
  document.documentElement.setAttribute("data-theme", getTheme());
}

// useThemeMode tracks the live theme for components that can't key off CSS
// tokens alone — e.g. React Flow's `colorMode`, which is a JS prop, not a
// CSS variable. Reads the current data-theme and re-renders when it flips
// (Settings toggles the attribute imperatively, with no React state).
export function useThemeMode(): ThemeMode {
  const [mode, setMode] = useState<ThemeMode>(getTheme);
  useEffect(() => {
    const root = document.documentElement;
    const read = () =>
      setMode(root.getAttribute("data-theme") === "light" ? "light" : "dark");
    read();
    const obs = new MutationObserver(read);
    obs.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
    return () => obs.disconnect();
  }, []);
  return mode;
}
