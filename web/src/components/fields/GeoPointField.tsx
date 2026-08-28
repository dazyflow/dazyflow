// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useRef, useState } from "react";
import L from "leaflet";
import "leaflet/dist/leaflet.css";
import { useTranslation } from "react-i18next";
import { Button } from "../ui/Button";

// GeoPointField is the editor widget behind a param with format:"geo-point"
// (the Location drop's `point`). It renders an OpenStreetMap map: search for a
// place, then click or drag the pin to fine-tune. The value it reads/writes is
// the canonical "lat,lon" string the Weather/geo drops speak.
//
// When a sibling Place (a city/address) is set, the pin FOLLOWS it: the Place
// is geocoded client-side and the marker moves there, mirroring the run-time
// behaviour where a Place overrides the map pin. In that mode manual picking is
// off (the Place wins); clear the Place to pick on the map again.
//
// Tiles come from OpenStreetMap's public server and search from Nominatim —
// both free and key-less, used here only at design time (one tile fetch per
// pan, one search per place/typed query), within their fair-use policy.
// Attribution ("© OpenStreetMap contributors") is shown, as their licence
// requires.

const OSM_TILES = "https://tile.openstreetmap.org/{z}/{x}/{y}.png";
const NOMINATIM = "https://nominatim.openstreetmap.org/search";

function parsePoint(v: string): { lat: number; lon: number } | null {
  const m = v.split(",");
  if (m.length !== 2) return null;
  const lat = parseFloat(m[0].trim());
  const lon = parseFloat(m[1].trim());
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  if (lat < -90 || lat > 90 || lon < -180 || lon > 180) return null;
  return { lat, lon };
}

// trim trailing zeros: 59.32930 → 59.3293, but keep enough precision (~1 m).
const fmt = (n: number) => String(Math.round(n * 1e6) / 1e6);
const fmtPoint = (lat: number, lon: number) => `${fmt(lat)},${fmt(lon)}`;

// A dependency-free marker: Leaflet's default PNG marker needs bundler asset
// wiring, so we use a CSS/emoji divIcon instead.
const markerIcon = L.divIcon({
  className: "geo-pin",
  html: "📍",
  iconSize: [24, 24],
  iconAnchor: [12, 23],
});

// geocodeQuery resolves a place name to its best match (or null). Used both by
// the manual search box and by the Place-follow effect.
async function geocodeQuery(q: string): Promise<{ lat: number; lon: number; name: string } | null> {
  const r = await fetch(`${NOMINATIM}?format=jsonv2&limit=1&q=${encodeURIComponent(q)}`, {
    headers: { Accept: "application/json" },
  });
  const hits = (await r.json()) as Array<{ lat: string; lon: string; display_name?: string }>;
  if (!Array.isArray(hits) || hits.length === 0) return null;
  const lat = parseFloat(hits[0].lat);
  const lon = parseFloat(hits[0].lon);
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return null;
  return { lat, lon, name: hits[0].display_name ?? q };
}

export function GeoPointField({
  value,
  onChange,
  place,
  placeWired,
  runCoordinate,
}: {
  value: string;
  onChange: (v: string) => void;
  // The sibling Place (city/address). When set, the pin follows it.
  place?: string;
  // True when the Place INPUT PORT is wired from another step. Then the
  // location is decided at run time, so we stop following the typed Place and
  // say so — neither the typed Place nor the map pin is used.
  placeWired?: boolean;
  // The coordinate this node emitted on its last run ("lat,lon"). When set, the
  // pin recenters on it — so after running, the map shows where it landed
  // (especially useful when the location came from a wired input).
  runCoordinate?: string;
}) {
  const { t } = useTranslation();
  const mapEl = useRef<HTMLDivElement>(null);
  const mapRef = useRef<L.Map | null>(null);
  const markerRef = useRef<L.Marker | null>(null);
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const [query, setQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [searchErr, setSearchErr] = useState<string | null>(null);
  const [placeName, setPlaceName] = useState<string | null>(null);
  const [placeErr, setPlaceErr] = useState<string | null>(null);

  // `place` is the EFFECTIVE place to show: the typed Place param, or — when
  // the Place input is wired from a resolvable literal (a Text drop) — that
  // upstream value (the caller resolves it). So we geocode and follow it
  // whenever it's non-empty, wired or not.
  const placeQuery = (place ?? "").trim();
  const following = placeQuery !== "";
  // The pin can't be picked manually while a Place (typed or wired) owns it.
  const readOnly = !!placeWired || following;
  // Read inside the map-click closure (registered once) without stale capture.
  const readOnlyRef = useRef(readOnly);
  readOnlyRef.current = readOnly;

  const parsed = parsePoint(value);

  // setMarker places or moves the (single) marker, optionally recentering.
  function setMarker(lat: number, lon: number, recenter = false) {
    const map = mapRef.current;
    if (!map) return;
    if (!markerRef.current) {
      markerRef.current = L.marker([lat, lon], { draggable: true, icon: markerIcon }).addTo(map);
      markerRef.current.on("dragend", () => {
        if (readOnlyRef.current) return; // a Place owns the location; ignore drags
        const ll = markerRef.current!.getLatLng();
        onChangeRef.current(fmtPoint(ll.lat, ll.lng));
      });
    } else {
      markerRef.current.setLatLng([lat, lon]);
    }
    if (recenter) map.setView([lat, lon], Math.max(map.getZoom(), 12));
  }

  // init the map once.
  useEffect(() => {
    if (!mapEl.current || mapRef.current) return;
    const start = parsed ?? { lat: 20, lon: 0 };
    // keyboard:false so the map doesn't swallow Delete/arrow keys when focused
    // on the canvas — those belong to the flow editor (delete/move the node).
    const map = L.map(mapEl.current, { keyboard: false }).setView(
      [start.lat, start.lon],
      parsed ? 11 : 2,
    );
    L.tileLayer(OSM_TILES, {
      maxZoom: 19,
      attribution: "© OpenStreetMap contributors",
    }).addTo(map);
    map.on("click", (e: L.LeafletMouseEvent) => {
      if (readOnlyRef.current) return; // a Place owns the location; manual pick is off
      setMarker(e.latlng.lat, e.latlng.lng);
      onChangeRef.current(fmtPoint(e.latlng.lat, e.latlng.lng));
    });
    mapRef.current = map;
    if (parsed) setMarker(parsed.lat, parsed.lon);
    setTimeout(() => map.invalidateSize(), 0);
    return () => {
      map.remove();
      mapRef.current = null;
      markerRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // When NOT following a Place, keep the marker synced to the point value.
  useEffect(() => {
    if (following) return;
    if (parsed) setMarker(parsed.lat, parsed.lon);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, following]);

  // When a Place is set, geocode it (debounced) and move the pin there, so the
  // map mirrors the override the run-time will apply.
  useEffect(() => {
    if (!following) {
      setPlaceName(null);
      setPlaceErr(null);
      return;
    }
    let cancelled = false;
    setPlaceErr(null);
    const id = window.setTimeout(() => {
      void geocodeQuery(placeQuery)
        .then((hit) => {
          if (cancelled) return;
          if (!hit) {
            setPlaceName(null);
            setPlaceErr(t("geo.placeNotFound", { defaultValue: "Couldn't locate that place" }));
            return;
          }
          setPlaceName(hit.name);
          setMarker(hit.lat, hit.lon, true);
        })
        .catch(() => {
          if (!cancelled) setPlaceErr(t("geo.searchFailed", { defaultValue: "Lookup failed — try again" }));
        });
    }, 600);
    return () => {
      cancelled = true;
      window.clearTimeout(id);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [placeQuery, following]);

  // After a run, recenter the pin on the coordinate the node actually emitted
  // — the resolved location (a wired Place/Coordinate is only known now).
  useEffect(() => {
    if (!runCoordinate) return;
    const p = parsePoint(runCoordinate);
    if (p) setMarker(p.lat, p.lon, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runCoordinate]);

  async function search() {
    const q = query.trim();
    if (!q || searching) return;
    setSearching(true);
    setSearchErr(null);
    try {
      const hit = await geocodeQuery(q);
      if (!hit) {
        setSearchErr(t("geo.noMatch", { defaultValue: "No place found" }));
        return;
      }
      setMarker(hit.lat, hit.lon, true);
      onChangeRef.current(fmtPoint(hit.lat, hit.lon));
    } catch {
      setSearchErr(t("geo.searchFailed", { defaultValue: "Search failed — try again" }));
    } finally {
      setSearching(false);
    }
  }

  return (
    <div className="geo-point-field">
      {following ? (
        // A Place (typed, or resolved from a wired Text) drives the pin.
        <div className="geo-following">
          {placeErr
            ? placeErr
            : placeName
              ? placeWired
                ? t("geo.followingWired", { defaultValue: "Showing wired Place: {{name}}", name: placeName })
                : t("geo.followingPlace", { defaultValue: "Showing Place: {{name}}", name: placeName })
              : t("geo.locating", { defaultValue: "Locating {{q}}…", q: placeQuery })}
        </div>
      ) : placeWired ? (
        // Wired from a dynamic source we can't resolve at design time.
        <div className="geo-following">
          {t("geo.wiredInput", {
            defaultValue: "Location comes from the wired input — set at run time",
          })}
        </div>
      ) : (
        <div className="geo-search">
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void search();
              }
            }}
            placeholder={t("geo.searchPlaceholder", { defaultValue: "Search a place…" })}
          />
          <Button size="sm" onClick={() => void search()} disabled={searching || query.trim() === ""}>
            {searching
              ? t("geo.searching", { defaultValue: "Searching…" })
              : t("geo.search", { defaultValue: "Search" })}
          </Button>
        </div>
      )}
      {searchErr && !readOnly && <div className="geo-search-err">{searchErr}</div>}
      <div ref={mapEl} className={"geo-map" + (readOnly ? " geo-map-readonly" : "")} />
      <div className="geo-coord">
        {following
          ? placeWired
            ? t("geo.wiredFollowHint", { defaultValue: "Showing the wired Place — it sets the location at run time" })
            : t("geo.placeWins", { defaultValue: "Pin follows the Place — clear it to pick on the map" })
          : placeWired
            ? t("geo.wiredHint", { defaultValue: "The wired Place sets the location at run time" })
            : value
              ? value
              : t("geo.hint", { defaultValue: "Search, or click the map to drop a pin" })}
      </div>
    </div>
  );
}
