// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Theme has two layers, and keeping them apart is the whole design:
//
//   ThemeMode  — what the USER chose: "system" (the default), "dark", or
//                "light". Persisted per browser in localStorage and roamed
//                to the account via /me/preferences.
//   ResolvedTheme — what actually gets painted: "dark" or "light", never
//                "system". This is what lands on <html data-theme>, so every
//                CSS token keys off a concrete value and the stylesheet
//                needs no prefers-color-scheme rules of its own.
//
// "system" resolves through prefers-color-scheme and re-resolves live when
// the OS flips (see watchSystemTheme). Defaulting to the OS rather than to
// dark matters: most people run their machine in light mode, and forcing a
// near-black violet app on them on first sign-in reads as broken, not
// styled.
import { useEffect, useState } from "react";

export type ThemeMode = "system" | "dark" | "light";
export type ResolvedTheme = "dark" | "light";

const KEY = "dazyflow.theme";

// prefersDark reads the OS setting. Guarded for non-browser/older
// environments (SSR, jsdom without matchMedia) — those fall back to dark,
// which is the app's historical look.
function prefersDark(): boolean {
  if (typeof window === "undefined" || !window.matchMedia) return true;
  return window.matchMedia("(prefers-color-scheme: dark)").matches;
}

// getThemeMode returns the user's stored CHOICE. Anything unrecognised —
// including the "" the server uses for "no explicit choice" and any value
// written by a build that predates this — means "follow the system".
export function getThemeMode(): ThemeMode {
  try {
    const v = localStorage.getItem(KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* storage blocked — fall through to the default */
  }
  return "system";
}

// resolveTheme collapses a mode to the concrete theme to paint.
export function resolveTheme(mode: ThemeMode): ResolvedTheme {
  if (mode === "dark" || mode === "light") return mode;
  return prefersDark() ? "dark" : "light";
}

// getTheme returns the theme currently being painted (never "system").
export function getTheme(): ResolvedTheme {
  return resolveTheme(getThemeMode());
}

// applyTheme records the choice and stamps the resolved theme on <html>.
export function applyTheme(mode: ThemeMode): void {
  document.documentElement.setAttribute("data-theme", resolveTheme(mode));
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    /* non-essential — the attribute is already applied for this session */
  }
}

// watchSystemTheme keeps a "system" user in sync when the OS flips light/dark
// mid-session (macOS/Windows auto-switching at dusk, most commonly). Re-reads
// the stored mode on every event so a later explicit choice wins without
// needing to tear the listener down. Registered once from initTheme.
function watchSystemTheme(): void {
  if (typeof window === "undefined" || !window.matchMedia) return;
  const mq = window.matchMedia("(prefers-color-scheme: dark)");
  const onChange = () => {
    if (getThemeMode() !== "system") return;
    document.documentElement.setAttribute("data-theme", prefersDark() ? "dark" : "light");
  };
  // addEventListener on MediaQueryList is the modern API; Safari < 14 only
  // has the deprecated addListener. Feature-detect rather than assume.
  if (typeof mq.addEventListener === "function") {
    mq.addEventListener("change", onChange);
  } else if (typeof mq.addListener === "function") {
    mq.addListener(onChange);
  }
}

// initTheme runs once at boot (before React renders) so the first paint
// matches the saved choice rather than the data-theme baked into
// index.html.
export function initTheme(): void {
  document.documentElement.setAttribute("data-theme", getTheme());
  watchSystemTheme();
}

// useThemeMode tracks the live RESOLVED theme for components that can't key
// off CSS tokens alone — e.g. React Flow's `colorMode`, which is a JS prop,
// not a CSS variable. Reads the current data-theme and re-renders when it
// flips, whether that came from the Settings picker or from the OS changing
// under a "system" user (both routes write the same attribute).
export function useThemeMode(): ResolvedTheme {
  const [mode, setMode] = useState<ResolvedTheme>(getTheme);
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
