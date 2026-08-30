// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Every system note the daemon can emit must have Swedish, or it silently
// reverts to the daemon's English in the middle of a translated thread —
// which is the exact bug the system_code field was added to fix, reappearing
// one note at a time as new ones are introduced.
//
// The code list is read from core/ticket.go rather than restated here, so a
// note added on the Go side fails this test until its copy is written. That is
// the whole point: a mirrored list would drift and pass.

import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import en from "../../i18n/en.json";
import sv from "../../i18n/sv.json";

const GO = join(__dirname, "../../../../core/ticket.go");
const src = readFileSync(GO, "utf8");

// The SystemNote constants: `NoteCustomerClosed SystemNote = "customer_closed"`
const codes = [...src.matchAll(/SystemNote\s*=\s*"([a-z_]+)"/g)].map((m) => m[1]);
// MarkedNote(s) builds "marked_<status>" for every TicketStatus, so the codes
// it can produce are exactly the statuses the ticket model defines.
const statuses = [...src.matchAll(/TicketStatus\s*=\s*"([a-z_]+)"/g)].map((m) => m[1]);
const all = [...codes, ...statuses.map((s) => `marked_${s}`)];

// The renderer's map, read the same way — from the source, not restated.
const TSX_SRC = readFileSync(join(__dirname, "SupportTickets.tsx"), "utf8");
const mapped = new Set(
  [...TSX_SRC.matchAll(/^\s{2}([a-z_]+): "(support\.note\.[A-Za-z]+)",$/gm)].map((m) => m[1]),
);
const keys = new Set(
  [...TSX_SRC.matchAll(/"(support\.note\.[A-Za-z]+)"/g)].map((m) => m[1]),
);

const lookup = (bundle: unknown, dotted: string) =>
  dotted.split(".").reduce<unknown>((o, k) => (o as Record<string, unknown>)?.[k], bundle);

describe("support system notes", () => {
  it("finds the codes the daemon defines", () => {
    // A rename on the Go side that emptied these would make every assertion
    // below pass while checking nothing.
    expect(codes.length).toBeGreaterThan(2);
    expect(statuses.length).toBeGreaterThan(3);
  });

  it("has a rendering for every code the daemon can emit", () => {
    expect(
      all.filter((c) => !mapped.has(c)),
      "add these to SYSTEM_NOTE in SupportTickets.tsx — an unmapped code " +
        "falls back to the daemon's English",
    ).toEqual([]);
  });

  it("has English and Swedish for every key the renderer uses", () => {
    const missing = [...keys].filter(
      (k) => !lookup(en, k) || !lookup(sv, k),
    );
    expect(missing, "write the copy in i18n/en.json and i18n/sv.json").toEqual([]);
  });

  it("maps no code the daemon cannot produce", () => {
    // A note removed on the Go side leaves a mapping behind, where it reads as
    // coverage of something that no longer happens.
    const live = new Set(all);
    expect([...mapped].filter((c) => !live.has(c))).toEqual([]);
  });
});
