// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Vitest setup shared by all tests. Adds jest-dom matchers (toBeInTheDocument,
// toHaveTextContent, …), installs a working localStorage, and clears the DOM +
// mocks + storage between tests so component tests don't leak state into each
// other.
import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";
import { cleanup } from "@testing-library/react";

// Node ships a `localStorage` global that shadows jsdom's and throws on every
// method when the process was started without a valid --localstorage-file, so
// anything reading storage (the auth session marker, the active-tenant key,
// theme/language caches) blows up mid-render. Install a plain in-memory
// implementation over it; the afterEach below empties it between tests.
const storage = new Map<string, string>();
Object.defineProperty(globalThis, "localStorage", {
  configurable: true,
  value: {
    get length() {
      return storage.size;
    },
    key: (i: number) => [...storage.keys()][i] ?? null,
    getItem: (k: string) => storage.get(String(k)) ?? null,
    setItem: (k: string, v: string) => void storage.set(String(k), String(v)),
    removeItem: (k: string) => void storage.delete(String(k)),
    clear: () => storage.clear(),
  } satisfies Storage,
});

// jsdom implements no layout, so Element.scrollIntoView is missing entirely —
// any component that scrolls a chat thread or list to the bottom on mount
// throws in tests. Stub it once here rather than in each test file.
if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

// xterm.js parses CSS colours by painting them onto a 1×1 canvas and reading
// the pixel back, which it does at MODULE LOAD — so merely importing anything
// that reaches LiveConsole or the system-log viewer makes jsdom log a
// not-implemented stack trace for getContext. jsdom returns null rather than
// throwing, so nothing fails; the five flow-editor suites just each printed a
// ten-frame trace that reads like a real error in CI output.
//
// The stub is the smallest surface xterm actually uses: set a fill, fill a
// rect, read four bytes back. Deliberately NOT a colour implementation —
// nothing in a jsdom test asserts on a rendered terminal, and pretending to
// measure one would invite tests that trust the numbers.
HTMLCanvasElement.prototype.getContext = function getContext() {
  return {
    fillStyle: "",
    fillRect: () => {},
    clearRect: () => {},
    getImageData: () => ({ data: new Uint8ClampedArray(4) }),
    measureText: () => ({ width: 0 }),
    createLinearGradient: () => ({ addColorStop: () => {} }),
  } as unknown as CanvasRenderingContext2D;
} as unknown as typeof HTMLCanvasElement.prototype.getContext;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  localStorage.clear();
});
