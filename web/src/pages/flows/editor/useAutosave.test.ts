// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { Graph } from "../../../types";

const saveGraph = vi.fn();

vi.mock("../../../api", () => {
  class APIError extends Error {
    status = 0;
    code = "";
  }
  return {
    APIError,
    authHeader: () => ({}),
    isHTTPStatus: () => false,
    isErrorCode: () => false,
    api: { saveGraph: (...a: unknown[]) => saveGraph(...a) },
  };
});

import { useAutosave } from "./useAutosave";

const graph = { id: "f", tenant: "t", workspace: "w", nodes: [], edges: [] } as unknown as Graph;

function setup() {
  return renderHook(() =>
    useAutosave({
      token: "cookie-session",
      ready: true,
      graphID: "f",
      t: (k) => k,
      buildGraph: () => graph,
      canEdit: false, // keeps the unmount flush out of the picture
      lockedRunID: null,
      previewing: false,
      loadedID: { current: "f" },
      onSaved: () => {},
      onError: () => {},
      onConflict: () => {},
      reArmOn: [],
    }),
  );
}

describe("useAutosave", () => {
  it("clears dirty when nothing changed during the save", async () => {
    saveGraph.mockResolvedValueOnce({});
    const { result } = setup();
    act(() => result.current.setDirty(true));
    await act(async () => {
      await result.current.save();
    });
    expect(result.current.dirty).toBe(false);
  });

  // The PUT carries the graph as it was when the save started; an edit made
  // while it was in flight is not in it and must stay dirty to be saved next.
  it("stays dirty when an edit lands while the save is in flight", async () => {
    let finish: (v: unknown) => void = () => {};
    saveGraph.mockReturnValueOnce(new Promise((r) => (finish = r)));
    const { result } = setup();
    act(() => result.current.setDirty(true));
    let pending: Promise<boolean> = Promise.resolve(false);
    act(() => {
      pending = result.current.save();
    });
    act(() => result.current.setDirty(true));
    await act(async () => {
      finish({});
      await pending;
    });
    expect(result.current.dirty).toBe(true);
  });

  // A remote save is waiting for the user to choose whose version to keep:
  // the idle autosave must not choose for them.
  it("does not autosave while held, and does once released", async () => {
    vi.useFakeTimers();
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response(null))));
    saveGraph.mockReset();
    saveGraph.mockResolvedValue({});
    const { result, rerender } = renderHook(({ held }) =>
      useAutosave({
        token: "cookie-session",
        ready: true,
        graphID: "f",
        t: (k) => k,
        buildGraph: () => graph,
        canEdit: true,
        lockedRunID: null,
        previewing: false,
        held,
        loadedID: { current: "f" },
        onSaved: () => {},
        onError: () => {},
        onConflict: () => {},
        reArmOn: [],
      }),
      { initialProps: { held: true } },
    );
    act(() => result.current.setDirty(true));
    await act(async () => {
      vi.advanceTimersByTime(5000);
    });
    expect(saveGraph).not.toHaveBeenCalled();
    rerender({ held: false });
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(saveGraph).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });
});
