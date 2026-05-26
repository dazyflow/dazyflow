// Theme mode: dark (default, the synthwave editor look) or light (the
// designed-fresh professional palette). Persisted per browser in
// localStorage and applied as data-theme on <html>, which every CSS
// token keys off. No server-side user setting yet — browser-local only.
export type ThemeMode = "dark" | "light";

const KEY = "hazyflow.theme";

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
