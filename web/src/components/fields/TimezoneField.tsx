// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useId, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

// TimezoneField is the editor behind a param with format:"timezone" (the Date &
// time step's `tz`). Type to filter, arrow keys to move, Enter to pick.
//
// A timezone is the wrong shape for a dropdown: with over four hundred IANA
// zones a <select> holds a curated handful plus an "other" escape hatch, so the
// common case is a list without your zone and the real case is typing a name
// from memory with no confirmation until a run fails.
//
// The list comes from the browser (Intl.supportedValuesOf) rather than a bundled
// table, because the tz database changes a few times a year and a shipped table
// would need a release to fix. The fallback below is only for engines without
// the enumeration API.
//
// Each row shows the zone's CURRENT offset, because that is what people
// recognise a zone by, and what tells Europe/Dublin from Europe/London in the
// two months a year they differ.

const FALLBACK_ZONES = [
  "UTC",
  "Europe/Stockholm",
  "Europe/Oslo",
  "Europe/Copenhagen",
  "Europe/Helsinki",
  "Europe/London",
  "Europe/Dublin",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Madrid",
  "Europe/Rome",
  "Europe/Warsaw",
  "Europe/Athens",
  "Europe/Istanbul",
  "Europe/Moscow",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Toronto",
  "America/Mexico_City",
  "America/Sao_Paulo",
  "Africa/Cairo",
  "Africa/Lagos",
  "Africa/Nairobi",
  "Africa/Johannesburg",
  "Asia/Jerusalem",
  "Asia/Dubai",
  "Asia/Karachi",
  "Asia/Kolkata",
  "Asia/Bangkok",
  "Asia/Singapore",
  "Asia/Hong_Kong",
  "Asia/Shanghai",
  "Asia/Seoul",
  "Asia/Tokyo",
  "Australia/Perth",
  "Australia/Brisbane",
  "Australia/Sydney",
  "Pacific/Auckland",
];

// MAX_ROWS caps how many matches render at once. Unfiltered that is four
// hundred rows of DOM in a side panel, and nobody scrolls a list that long —
// they type. The count of what was left out is shown, so the cap never reads
// as "that's all there is".
const MAX_ROWS = 60;

function allZones(): string[] {
  try {
    const zones = Intl.supportedValuesOf?.("timeZone");
    if (zones?.length) return ["UTC", ...zones.filter((z) => z !== "UTC")];
  } catch {
  }
  return FALLBACK_ZONES;
}

// offsetOf renders a zone's current UTC offset ("GMT+2"). Cached because the
// Intl formatter is expensive enough to notice across a filtered list and the
// answer doesn't change while a form is open.
const offsetCache = new Map<string, string>();
function offsetOf(zone: string): string {
  const hit = offsetCache.get(zone);
  if (hit !== undefined) return hit;
  let out = "";
  try {
    const parts = new Intl.DateTimeFormat("en-US", {
      timeZone: zone,
      timeZoneName: "shortOffset",
    }).formatToParts(new Date());
    out = parts.find((p) => p.type === "timeZoneName")?.value ?? "";
  } catch {
    out = "";
  }
  offsetCache.set(zone, out);
  return out;
}

const searchable = (s: string) => s.toLowerCase().replace(/_/g, " ");

export function TimezoneField({
  value,
  onChange,
}: {
  value: string;
  onChange: (v: string) => void;
}) {
  const { t } = useTranslation();
  const zones = useMemo(allZones, []);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const listID = useId();

  const matches = useMemo(() => {
    const q = searchable(query.trim());
    if (!q) return zones;
    return zones.filter((z) => searchable(z).includes(q));
  }, [zones, query]);
  const shown = matches.slice(0, MAX_ROWS);
  const hidden = matches.length - shown.length;

  const commit = (zone: string) => {
    onChange(zone);
    setQuery("");
    setOpen(false);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) {
        setOpen(true);
        return;
      }
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) =>
        Math.min(Math.max(i + step, 0), Math.max(shown.length - 1, 0)),
      );
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      // The highlighted row when there is one; otherwise whatever was typed.
      // That second path is the escape hatch this field must keep: a zone this
      // browser's list doesn't carry (an old engine, a brand-new zone) and the
      // special names the drop accepts but no picker can list, like "Local".
      // A name that is neither fails the step with a clear message at run time.
      const pick = shown[active] ?? query.trim();
      if (pick) commit(pick);
      return;
    }
    if (e.key === "Escape" && open) {
      e.stopPropagation();
      setQuery("");
      setOpen(false);
    }
  };

  return (
    <div className="tz-field">
      <input
        ref={inputRef}
        className="tz-input"
        type="text"
        role="combobox"
        aria-expanded={open}
        aria-controls={listID}
        aria-autocomplete="list"
        aria-activedescendant={
          open && shown[active] ? `${listID}-${active}` : undefined
        }
        placeholder={t("schemaForm.tzPlaceholder")}
        value={open ? query : value}
        onFocus={() => {
          setOpen(true);
          setActive(0);
        }}
        // Blur reverts to the stored value rather than committing the typed
        // text: half a zone name is not a zone, and silently saving one would
        // put a run-time error in a flow the author believes they configured.
        onBlur={() => {
          setQuery("");
          setOpen(false);
        }}
        onChange={(e) => {
          setQuery(e.target.value);
          setOpen(true);
          setActive(0);
        }}
        onKeyDown={onKeyDown}
      />
      {open && (
        <ul className="tz-list" id={listID} role="listbox">
          {shown.map((zone, i) => (
            <li
              key={zone}
              id={`${listID}-${i}`}
              role="option"
              aria-selected={zone === value}
              className={"tz-option" + (i === active ? " active" : "")}
              // mousedown, not click: the input's blur would close the list
              // before a click ever landed.
              onMouseDown={(e) => {
                e.preventDefault();
                commit(zone);
              }}
              onMouseEnter={() => setActive(i)}
            >
              <span className="tz-name">{zone}</span>
              <span className="tz-offset">{offsetOf(zone)}</span>
            </li>
          ))}
          {shown.length === 0 && (
            <li className="tz-empty">{t("schemaForm.tzNoMatch")}</li>
          )}
          {hidden > 0 && (
            <li className="tz-more">
              {t("schemaForm.tzMore", { count: hidden })}
            </li>
          )}
        </ul>
      )}
    </div>
  );
}
