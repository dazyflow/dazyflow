// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// The theme resolver has two layers (stored CHOICE vs painted THEME) and a
// fallback matrix that's easy to regress: a stale "" from the server, a
// missing matchMedia, an OS that flips mid-session. Pin all of it.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, getTheme, getThemeMode, initTheme, resolveTheme } from "./theme";

// mockMatchMedia installs a matchMedia whose result tracks `dark`, and hands
// back a fire() that replays the OS flipping to every registered listener.
function mockMatchMedia(dark: boolean) {
  const listeners: (() => void)[] = [];
  const mq = {
    get matches() {
      return dark;
    },
    addEventListener: (_: string, fn: () => void) => void listeners.push(fn),
    removeEventListener: vi.fn(),
  };
  vi.stubGlobal("matchMedia", () => mq);
  return {
    flip(next: boolean) {
      dark = next;
      listeners.forEach((fn) => fn());
    },
  };
}

beforeEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute("data-theme");
});

describe("theme mode", () => {
  it("defaults to system when nothing is stored", () => {
    expect(getThemeMode()).toBe("system");
  });

  it("treats the server's empty-string 'no choice' as system", () => {
    localStorage.setItem("dazyflow.theme", "");
    expect(getThemeMode()).toBe("system");
  });

  it("keeps an explicit choice", () => {
    localStorage.setItem("dazyflow.theme", "light");
    expect(getThemeMode()).toBe("light");
  });
});

describe("resolveTheme", () => {
  it("follows the OS when the mode is system", () => {
    mockMatchMedia(false);
    expect(resolveTheme("system")).toBe("light");
    mockMatchMedia(true);
    expect(resolveTheme("system")).toBe("dark");
  });

  it("ignores the OS when the user picked a theme", () => {
    mockMatchMedia(true);
    expect(resolveTheme("light")).toBe("light");
    mockMatchMedia(false);
    expect(resolveTheme("dark")).toBe("dark");
  });

  it("falls back to dark where matchMedia is unavailable", () => {
    vi.stubGlobal("matchMedia", undefined);
    expect(resolveTheme("system")).toBe("dark");
  });
});

describe("applyTheme", () => {
  it("stores the choice but stamps the resolved theme", () => {
    mockMatchMedia(false);
    applyTheme("system");
    expect(localStorage.getItem("dazyflow.theme")).toBe("system");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    expect(getTheme()).toBe("light");
  });
});

describe("initTheme", () => {
  it("repaints a system user when the OS flips mid-session", () => {
    const os = mockMatchMedia(false);
    localStorage.setItem("dazyflow.theme", "system");
    initTheme();
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    os.flip(true);
    expect(document.documentElement.getAttribute("data-theme")).toBe("dark");
  });

  it("leaves an explicit choice alone when the OS flips", () => {
    const os = mockMatchMedia(false);
    localStorage.setItem("dazyflow.theme", "light");
    initTheme();
    os.flip(true);
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
  });
});
