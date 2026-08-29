// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { MOBILE, EDITOR_NARROW, isNarrower, mediaQuery } from "./breakpoints";
import { savedCollapsePref, initialNavCollapsed } from "./navCollapse";

// jsdom's window.innerWidth is writable, so the viewport can be set per case.
function setWidth(px: number) {
  Object.defineProperty(window, "innerWidth", {
    value: px,
    writable: true,
    configurable: true,
  });
}

describe("breakpoints", () => {
  const realWidth = window.innerWidth;
  afterEach(() => setWidth(realWidth));

  // The boundary is inclusive: `isNarrower(768)` mirrors
  // `@media (max-width: 768px)`, which matches AT 768.
  it("is inclusive at the breakpoint", () => {
    setWidth(MOBILE);
    expect(isNarrower(MOBILE)).toBe(true);
    setWidth(MOBILE - 1);
    expect(isNarrower(MOBILE)).toBe(true);
    setWidth(MOBILE + 1);
    expect(isNarrower(MOBILE)).toBe(false);
  });

  it("treats the two breakpoints independently", () => {
    // A tablet-width viewport is narrow for the editor but not for the shell.
    setWidth(900);
    expect(isNarrower(MOBILE)).toBe(false);
    expect(isNarrower(EDITOR_NARROW)).toBe(true);
  });

  // mediaQuery exists so a call site can't drift from the max-width form the
  // stylesheets use — check-css-breakpoints.mjs matches on exactly this shape.
  it("builds the max-width query the stylesheets use", () => {
    expect(mediaQuery(MOBILE)).toBe("(max-width: 768px)");
    expect(mediaQuery(EDITOR_NARROW)).toBe("(max-width: 1100px)");
  });
});

describe("savedCollapsePref", () => {
  const KEY = "test.nav.collapsed";

  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it("reads only the exact '1' marker as collapsed", () => {
    expect(savedCollapsePref(KEY)).toBe(false); // unset
    localStorage.setItem(KEY, "1");
    expect(savedCollapsePref(KEY)).toBe(true);
    // Anything else is not the stored form and must read as expanded rather
    // than as truthy-ish.
    for (const v of ["0", "true", "", "yes", " 1"]) {
      localStorage.setItem(KEY, v);
      expect(savedCollapsePref(KEY)).toBe(false);
    }
  });

  it("keys the two sidebars independently", () => {
    // The app shell and the docs shell are separate preferences — that is the
    // one thing the key parameter exists to keep apart.
    localStorage.setItem("app.nav", "1");
    expect(savedCollapsePref("app.nav")).toBe(true);
    expect(savedCollapsePref("docs.nav")).toBe(false);
  });

  // Private mode and strict-mode iframes make localStorage ACCESS throw, not
  // just return null. Defaulting to expanded is the documented behaviour; this
  // renders in the app shell, so throwing here would blank the whole page.
  it("defaults to expanded when storage access throws", () => {
    const real = Object.getOwnPropertyDescriptor(window, "localStorage");
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      get() {
        throw new DOMException("denied", "SecurityError");
      },
    });
    try {
      expect(savedCollapsePref(KEY)).toBe(false);
    } finally {
      if (real) Object.defineProperty(window, "localStorage", real);
    }
  });
});

describe("initialNavCollapsed", () => {
  const KEY = "test.nav.collapsed";
  const realWidth = window.innerWidth;

  beforeEach(() => localStorage.clear());
  afterEach(() => {
    localStorage.clear();
    setWidth(realWidth);
  });

  it("collapses on a narrow viewport whatever was saved", () => {
    setWidth(MOBILE - 100);
    expect(initialNavCollapsed(KEY)).toBe(true);
    // Even an explicit "expanded" desktop choice loses: there isn't room.
    localStorage.setItem(KEY, "0");
    expect(initialNavCollapsed(KEY)).toBe(true);
  });

  it("honours the saved choice on a wide viewport", () => {
    setWidth(MOBILE + 400);
    expect(initialNavCollapsed(KEY)).toBe(false);
    localStorage.setItem(KEY, "1");
    expect(initialNavCollapsed(KEY)).toBe(true);
  });

  // The key invariant: a narrow viewport collapses the sidebar for this paint
  // but must NOT rewrite the stored desktop preference. Resizing a window down
  // and back up has to return you to the layout you chose.
  it("does not persist a viewport-driven collapse", () => {
    localStorage.setItem(KEY, "0");
    setWidth(MOBILE - 100);
    expect(initialNavCollapsed(KEY)).toBe(true);
    expect(localStorage.getItem(KEY)).toBe("0");

    setWidth(MOBILE + 400);
    expect(initialNavCollapsed(KEY)).toBe(false);
  });

  it("collapses exactly at the breakpoint", () => {
    setWidth(MOBILE);
    expect(initialNavCollapsed(KEY)).toBe(true);
    setWidth(MOBILE + 1);
    expect(initialNavCollapsed(KEY)).toBe(false);
  });
});
