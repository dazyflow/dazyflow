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

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  localStorage.clear();
});
