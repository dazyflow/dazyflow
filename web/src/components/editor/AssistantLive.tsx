// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Bot } from "lucide-react";
import { ICON } from "../../icons";

// The live view of an assistant (Claude, over MCP) building the flow that is
// open on this canvas. Each assistant save arrives on the flow's watch stream
// naming the nodes it touched and a one-line note; the canvas lights those
// nodes up for a while and says what happened in a ticker, so someone watching
// on their phone can follow along without reading the graph.

// How long a touched node stays lit, and a ticker line stays up.
export const ASSISTANT_CHANGED_MS = 8000;
export const ASSISTANT_TICKER_MS = 20_000;
const MAX_TICKER = 4;

export type AssistantSave = {
  assistant?: boolean;
  note?: string;
  touched?: string[];
};

type TickerEntry = { id: number; added: number; changed: number; note: string };

export function useAssistantLive() {
  const [touched, setTouched] = useState<Set<string>>(() => new Set());
  const [ticker, setTicker] = useState<TickerEntry[]>([]);
  const seq = useRef(0);
  const timers = useRef<number[]>([]);

  useEffect(
    () => () => {
      timers.current.forEach((t) => window.clearTimeout(t));
    },
    [],
  );

  // announce is called once the assistant's save has been applied, with the
  // node ids the canvas had before it, to tell additions from changes.
  const announce = useCallback((ev: AssistantSave, before: Set<string>) => {
    if (!ev.assistant) return;
    const ids = ev.touched ?? [];
    const added = ids.filter((id) => !before.has(id)).length;
    const entry: TickerEntry = {
      id: ++seq.current,
      added,
      changed: ids.length - added,
      note: (ev.note ?? "").trim(),
    };
    setTouched(new Set(ids));
    setTicker((prev) => [entry, ...prev].slice(0, MAX_TICKER));
    timers.current.push(
      window.setTimeout(() => {
        setTouched((cur) => (cur.size && ids.every((id) => cur.has(id)) ? new Set() : cur));
      }, ASSISTANT_CHANGED_MS),
      window.setTimeout(() => {
        setTicker((prev) => prev.filter((e) => e.id !== entry.id));
      }, ASSISTANT_TICKER_MS),
    );
  }, []);

  return { touched, ticker, announce };
}

export function AssistantTicker({ entries }: { entries: TickerEntry[] }) {
  const { t } = useTranslation();
  if (entries.length === 0) return null;
  return (
    <ul className="assistant-ticker" aria-live="polite">
      {entries.map((e) => (
        <li key={e.id} className="assistant-ticker-entry">
          <Bot className="icon-inline" size={ICON.sm} aria-hidden="true" />
          <span>
            <span className="assistant-ticker-what">{describe(t, e)}</span>
            {e.note && <span className="assistant-ticker-note">{e.note}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}

function describe(t: (k: string, o?: Record<string, unknown>) => string, e: TickerEntry): string {
  if (e.added && e.changed) {
    return `${t("editor.assistant.added", { count: e.added })}, ${t("editor.assistant.alsoChanged", { count: e.changed })}`;
  }
  if (e.added) return t("editor.assistant.added", { count: e.added });
  if (e.changed) return t("editor.assistant.changed", { count: e.changed });
  return t("editor.assistant.edited");
}
