// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useEffect, useMemo, useRef, useState } from "react";
import type { Node as FlowNode, Edge as FlowEdge } from "@xyflow/react";
import { api } from "../api";
import type { Manifest } from "../types";
import { type DazyNodeData } from "../components/editor/nodeCardShared";

const RESOURCE_PICKER_KINDS: Record<string, { provider: string; kind: string }> = {
  "google-form": { provider: "google", kind: "forms" },
  "google-spreadsheet": { provider: "google", kind: "spreadsheets" },
  "google-drive-file": { provider: "google", kind: "drive-files" },
  "google-drive-folder": { provider: "google", kind: "drive-folders" },
  "google-calendar": { provider: "google", kind: "calendars" },
  "stripe-price": { provider: "stripe", kind: "prices" },
  "stripe-subscription": { provider: "stripe", kind: "subscriptions" },
  "stripe-payment-intent": { provider: "stripe", kind: "payment_intents" },
  "stripe-customer": { provider: "stripe", kind: "customers" },
  "fortnox-customer": { provider: "fortnox", kind: "customers" },
  "slack-channel": { provider: "slack", kind: "channels" },
  "homeassistant-entity": { provider: "homeassistant", kind: "entities" },
  "homeassistant-service": { provider: "homeassistant", kind: "services" },
};

type PickerSchema = { format?: string };
const pickerFormat = (sch: unknown): string => {
  const f = (sch as PickerSchema | undefined)?.format;
  return typeof f === "string" ? f : "";
};

type ResourceResolverInput = {
  nodes: FlowNode<DazyNodeData>[];
  edges: FlowEdge[];
  paramsByID: Record<string, Record<string, unknown>>;
  manifestByID: Map<string, Manifest>;
  token: string | null;
};

export function useResourceResolver({
  nodes,
  edges,
  paramsByID,
  manifestByID,
  token,
}: ResourceResolverInput): Map<string, Record<string, string>> {
  const [resourceNames, setResourceNames] = useState<Map<string, string>>(() => new Map());
  const fetchedResourceSets = useRef<Set<string>>(new Set());

  useEffect(() => {
    if (!token) return;
    const combos = new Map<string, { provider: string; kind: string; account?: string }>();
    for (const n of nodes) {
      const props = manifestByID.get((n.data as DazyNodeData).moduleID)?.params_schema?.properties;
      if (!props) continue;
      const p = paramsByID[n.id] ?? {};
      for (const [key, sch] of Object.entries(props)) {
        const picker = RESOURCE_PICKER_KINDS[pickerFormat(sch)];
        if (!picker || typeof p[key] !== "string" || !p[key]) continue;
        const account = typeof p.account === "string" ? p.account : undefined;
        combos.set(`${picker.provider}:${picker.kind}:${account ?? "default"}`, {
          provider: picker.provider,
          kind: picker.kind,
          account,
        });
      }
    }
    // Fetch each (kind, account) list once and merge id→name. No per-run
    // "live" guard: the fetchedResourceSets ref already dedups across the
    // effect's several settling re-runs, and gating the single in-flight
    // fetch on a per-run flag would discard its result when the effect
    // re-runs before it resolves (the ref then blocks any retry). Applying
    // the result unconditionally is safe — setResourceNames on an unmounted
    // editor is a no-op in React 18.
    for (const [ck, { provider, kind, account }] of combos) {
      if (fetchedResourceSets.current.has(ck)) continue;
      fetchedResourceSets.current.add(ck);
      api
        .listAccountResources(token, provider, kind, account)
        .then((r) => {
          setResourceNames((prev) => {
            const next = new Map(prev);
            for (const o of r.resources)
              next.set(`${provider}:${kind}:${account ?? "default"}:${o.id}`, o.name);
            // Bound the cache so a long editing session (many distinct
            // resources picked/abandoned) doesn't grow it without limit. Map
            // keeps insertion order, so dropping from the front evicts the
            // oldest-resolved names; an evicted card just falls back to the id.
            const MAX = 1000;
            if (next.size > MAX) {
              let drop = next.size - MAX;
              for (const k of next.keys()) {
                if (drop-- <= 0) break;
                next.delete(k);
              }
            }
            return next;
          });
        })
        .catch(() => {
          fetchedResourceSets.current.delete(ck);
        });
    }
  }, [nodes, paramsByID, manifestByID, token]);

  return useMemo(() => {
    const byId = new Map(nodes.map((n) => [n.id, n]));
    const propsOf = (id: string) =>
      manifestByID.get((byId.get(id)?.data as DazyNodeData | undefined)?.moduleID ?? "")
        ?.params_schema?.properties;
    const resolveOwn = (id: string, key: string): string | undefined => {
      const picker = RESOURCE_PICKER_KINDS[pickerFormat(propsOf(id)?.[key])];
      const pp = paramsByID[id] ?? {};
      if (!picker || typeof pp[key] !== "string" || !pp[key]) return undefined;
      const account = typeof pp.account === "string" ? pp.account : undefined;
      return resourceNames.get(
        `${picker.provider}:${picker.kind}:${account ?? "default"}:${pp[key] as string}`,
      );
    };
    // Incoming wire per (target, targetHandle) → its source port, so a picker
    // param fed by a wire can borrow the upstream step's resolved name.
    const incoming = new Map<string, { source: string; sourceHandle?: string }>();
    for (const e of edges) {
      if (e.target && e.targetHandle) {
        incoming.set(`${e.target}:${e.targetHandle}`, {
          source: e.source,
          sourceHandle: e.sourceHandle ?? undefined,
        });
      }
    }
    const resolveName = (id: string, key: string, seen = new Set<string>()): string | undefined => {
      const guard = `${id}:${key}`;
      if (seen.has(guard)) return undefined;
      seen.add(guard);
      const up = incoming.get(guard);
      if (up?.source && up.sourceHandle) return resolveName(up.source, up.sourceHandle, seen);
      return resolveOwn(id, key);
    };
    const m = new Map<string, Record<string, string>>();
    for (const n of nodes) {
      const props = propsOf(n.id);
      if (!props) continue;
      const labels: Record<string, string> = {};
      for (const [key, sch] of Object.entries(props)) {
        const picker = RESOURCE_PICKER_KINDS[pickerFormat(sch)];
        if (!picker) continue;
        const name = resolveName(n.id, key);
        if (name) labels[key] = name;
      }
      if (Object.keys(labels).length) m.set(n.id, labels);
    }
    return m;
  }, [nodes, paramsByID, manifestByID, resourceNames, edges]);
}
