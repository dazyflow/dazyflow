// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The map picker's tile + geocoder URLs come from the daemon, because the same
// two values build the app's Content-Security-Policy. Hardcoding either one in
// the browser is what broke the picker before: the policy allowed one host and
// the code called another, so the map came up blank and every search failed.

import { beforeEach, describe, expect, it, vi } from "vitest";

const getMapConfig = vi.fn();
vi.mock("./api", () => ({ api: { getMapConfig: () => getMapConfig() } }));

import { mapConfig, resetMapConfigForTest } from "./mapConfig";

describe("mapConfig", () => {
  beforeEach(() => {
    resetMapConfigForTest();
    getMapConfig.mockReset();
  });

  it("uses what the daemon reports", async () => {
    getMapConfig.mockResolvedValue({
      tile_url: "https://tiles.internal/{z}/{x}/{y}.png",
      geocoder_url: "https://nom.internal",
    });
    await expect(mapConfig()).resolves.toEqual({
      tileUrl: "https://tiles.internal/{z}/{x}/{y}.png",
      geocoderUrl: "https://nom.internal",
    });
  });

  // The client appends "/search", so a configured trailing slash must not
  // survive into a "//search" request the server may not route.
  it("trims a trailing slash off the geocoder base", async () => {
    getMapConfig.mockResolvedValue({
      tile_url: "https://tiles.internal/{z}/{x}/{y}.png",
      geocoder_url: "https://nom.internal/",
    });
    await expect(mapConfig()).resolves.toMatchObject({
      geocoderUrl: "https://nom.internal",
    });
  });

  // Every picker on the canvas asks; one round trip should serve them all.
  it("fetches once and memoises", async () => {
    getMapConfig.mockResolvedValue({ tile_url: "a", geocoder_url: "b" });
    await Promise.all([mapConfig(), mapConfig(), mapConfig()]);
    expect(getMapConfig).toHaveBeenCalledTimes(1);
  });

  // An older daemon has no /map/config route. Falling back to the public OSM
  // hosts keeps the picker working there instead of leaving it blank.
  it("falls back to the public OSM hosts when the call fails", async () => {
    getMapConfig.mockRejectedValue(new Error("404"));
    await expect(mapConfig()).resolves.toEqual({
      tileUrl: "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
      geocoderUrl: "https://nominatim.openstreetmap.org",
    });
  });

  it("falls back per-field when the daemon reports a blank", async () => {
    getMapConfig.mockResolvedValue({ tile_url: "", geocoder_url: "" });
    await expect(mapConfig()).resolves.toEqual({
      tileUrl: "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
      geocoderUrl: "https://nominatim.openstreetmap.org",
    });
  });
});
