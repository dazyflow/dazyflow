// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { api } from "./api";

// Where the flow editor's map picker (GeoPointField) fetches tiles and
// geocodes place names. The daemon decides — a deployment that self-hosts
// tiles or Nominatim sets DAZYFLOW_MAP_TILE_URL / DAZYFLOW_MAP_GEOCODER_URL,
// and the same values widen its Content-Security-Policy so the browser is
// actually allowed to reach them (daemon/mapconfig.go).
//
// Fetched once per page load and memoised: every map picker on the canvas
// wants the same two strings, and they can't change without a daemon restart.

export type MapConfig = {
  tileUrl: string;
  geocoderUrl: string;
};

const FALLBACK: MapConfig = {
  tileUrl: "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
  geocoderUrl: "https://nominatim.openstreetmap.org",
};

let cached: Promise<MapConfig> | null = null;

export function mapConfig(): Promise<MapConfig> {
  cached ??= api
    .getMapConfig()
    .then((c) => ({
      tileUrl: c.tile_url || FALLBACK.tileUrl,
      geocoderUrl: (c.geocoder_url || FALLBACK.geocoderUrl).replace(/\/+$/, ""),
    }))
    .catch(() => FALLBACK);
  return cached;
}

export function resetMapConfigForTest() {
  cached = null;
}
