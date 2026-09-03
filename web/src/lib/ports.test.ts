// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, it, expect } from "vitest";
import type { Port } from "../types";
import {
  DEFAULT_MAX_VARIADIC_FAN_IN,
  MAX_VARIADIC_FAN_IN,
  inputHasRoom,
  mimeCompatible,
  pickPort,
  spawnPort,
  portsConnectable,
  portKind,
  portCardinality,
  portTypeLabel,
  connectionHint,
} from "./ports";

describe("mimeCompatible", () => {
  it("treats an untyped side as anything", () => {
    expect(mimeCompatible(undefined, ["text/plain"])).toBe(true);
    expect(mimeCompatible(["text/plain"], [])).toBe(true);
  });
  it("requires an exact MIME overlap (mirrors the backend validator)", () => {
    expect(mimeCompatible(["text/plain"], ["text/plain"])).toBe(true);
    // Same family is NOT enough — must be the exact same MIME.
    expect(mimeCompatible(["text/markdown"], ["text/plain"])).toBe(false);
    expect(mimeCompatible(["application/json"], ["application/x-bool"])).toBe(false);
    expect(mimeCompatible(["image/png"], ["text/plain"])).toBe(false);
    // Overlap anywhere in the sets still connects.
    expect(mimeCompatible(["text/plain", "application/json"], ["application/json"])).toBe(true);
  });
});

describe("pickPort", () => {
  // ntfy as the engine surfaces it: WithPassthrough prepends an untyped
  // "pass" pin ahead of the real "body" input (labelled "Message"). Both
  // are untyped, so the old "first compatible port" logic landed on pass.
  const ntfyInputs: Port[] = [
    { port: "pass", label: "Pass-through" },
    { port: "body", label: "Message" },
  ];

  it("attaches a Text output to ntfy's Message input, not passthrough", () => {
    expect(pickPort(ntfyInputs, ["text/plain"], "in")).toBe("body");
  });

  it("prefers a real input even when the dragged port is untyped", () => {
    expect(pickPort(ntfyInputs, undefined, "in")).toBe("body");
  });

  it("wires an untyped (file) source to an untyped input, not a typed text field", () => {
    // Gmail as the engine surfaces it: a leading passthrough pin, three
    // text/plain fields, then the untyped variadic "attachments". Dragging a
    // file output (no MIME) must land on attachments, not "To".
    const gmailInputs: Port[] = [
      { port: "pass", label: "Pass-through" },
      { port: "to", label: "To", mime: ["text/plain"] },
      { port: "subject", label: "Subject", mime: ["text/plain"] },
      { port: "body", label: "Body", mime: ["text/plain"] },
      { port: "attachments", label: "Attachments" },
    ];
    expect(pickPort(gmailInputs, undefined, "in")).toBe("attachments");
  });

  it("prefers a strict MIME match over an untyped real input", () => {
    const inputs: Port[] = [
      { port: "pass" },
      { port: "extra" }, // untyped real input, listed first
      { port: "msg", mime: ["text/plain"] },
    ];
    expect(pickPort(inputs, ["text/plain"], "in")).toBe("msg");
  });

  it("falls back to the passthrough pin when no real input is compatible", () => {
    const inputs: Port[] = [
      { port: "pass" },
      { port: "img", mime: ["image/png"] },
    ];
    // A text source matches nothing real → pass (untyped) is the only fit.
    expect(pickPort(inputs, ["text/plain"], "in")).toBe("pass");
  });

  it("returns the fallback handle id when the drop has no ports", () => {
    expect(pickPort([], ["text/plain"], "in")).toBe("in");
    expect(pickPort(undefined, ["text/plain"], "out")).toBe("out");
  });
});

describe("spawnPort", () => {
  it("refuses to invent a port on a drop that has none on that side", () => {
    // The bug this exists for: dragging off a step's output, picking Text from
    // the palette, and getting a wire into "text_1.in". Text is a value source
    // — no inputs at all — so that port does not exist. React Flow draws no
    // edge for an unknown handle, so nothing appeared on the canvas to delete,
    // while every autosave from then on came back "invalid graph: edge 0: node
    // \"text_1\" (text) has no input port \"in\"" and the editor retried it
    // on a timer. null means "place it unwired".
    expect(spawnPort(undefined, ["text/plain"], false, "in")).toBeNull();
    expect(spawnPort([], ["text/plain"], false, "in")).toBeNull();
    expect(spawnPort(undefined, undefined, true, "out")).toBeNull();
  });

  it("lands a pass-pin drag on the new drop's pass pin", () => {
    const inputs: Port[] = [
      { port: "pass", label: "Pass-through" },
      { port: "body", label: "Message" },
    ];
    expect(spawnPort(inputs, undefined, true, "in")).toBe("pass");
    // Not a pass drag: the real input wins, as pickPort decides.
    expect(spawnPort(inputs, undefined, false, "in")).toBe("body");
  });

  it("falls back to a data port when the new drop has no pass pin", () => {
    const inputs: Port[] = [{ port: "url", mime: ["text/plain"] }];
    expect(spawnPort(inputs, undefined, true, "in")).toBe("url");
  });
});

describe("portsConnectable", () => {
  const out: Port[] = [
    { port: "out", mime: ["text/plain"] },
    { port: "json", mime: ["application/json"] },
    { port: "pass" }, // untyped exec pin
  ];
  const inp: Port[] = [
    { port: "in", mime: ["text/plain"] },
    { port: "body", mime: ["application/json"] },
  ];

  it("allows a MIME-compatible source→target pair", () => {
    expect(portsConnectable(out, "out", inp, "in")).toBe(true);
    expect(portsConnectable(out, "json", inp, "body")).toBe(true);
  });

  it("rejects a MIME-incompatible pair", () => {
    expect(portsConnectable(out, "out", inp, "body")).toBe(false); // text → json
  });

  it("defaults handles to out/in", () => {
    expect(portsConnectable(out, null, inp, null)).toBe(true); // out→in, both text
  });

  it("treats a missing/untyped pin as connectable", () => {
    expect(portsConnectable(out, "pass", inp, "in")).toBe(true); // untyped source
    expect(portsConnectable(out, "out", inp, "nope")).toBe(true); // unknown target port
    expect(portsConnectable(undefined, "out", inp, "in")).toBe(true); // comment node, no manifest
  });
});

describe("port kind × cardinality (data model)", () => {
  it("derives kind from mime", () => {
    expect(portKind({ mime: ["application/json"] })).toBe("item");
    expect(portKind({ mime: ["text/plain"] })).toBe("text");
    expect(portKind({ mime: ["application/x-bool"] })).toBe("bool");
    expect(portKind({ mime: ["application/pdf"] })).toBe("file");
    expect(portKind({ mime: [] })).toBe("any");
    expect(portKind({})).toBe("any");
  });
  it("cardinality from list flag", () => {
    expect(portCardinality({ list: true })).toBe("many");
    expect(portCardinality({})).toBe("one");
  });
  it("plain-language labels", () => {
    expect(portTypeLabel({ mime: ["application/json"], list: true })).toBe("Items (a table)");
    expect(portTypeLabel({ mime: ["application/json"] })).toBe("Item");
    expect(portTypeLabel({ mime: ["text/plain"] })).toBe("Text");
    expect(portTypeLabel({})).toBe("Anything");
  });
});

describe("connectionHint", () => {
  it("null when compatible or untyped", () => {
    expect(connectionHint({ port: "a", mime: ["text/plain"] }, { port: "b", mime: ["text/plain"] })).toBeNull();
    expect(connectionHint({ port: "a" }, { port: "b", mime: ["text/plain"] })).toBeNull();
  });
  it("suggests a bridge for Items→Text and Text→Items", () => {
    expect(connectionHint({ port: "rows", mime: ["application/json"] }, { port: "body", mime: ["text/plain"] }))
      .toMatch(/Make text from items/);
    expect(connectionHint({ port: "t", mime: ["text/plain"] }, { port: "rows", mime: ["application/json"] }))
      .toMatch(/Read fields from text/);
  });
  it("generic message for other kind clashes", () => {
    expect(connectionHint({ port: "ok", mime: ["application/x-bool"] }, { port: "body", mime: ["text/plain"] }))
      .toMatch(/don.t match/);
  });
});

describe("inputHasRoom", () => {
  const single: Port[] = [{ port: "in", mime: ["text/plain"] }];
  const variadic: Port[] = [{ port: "items", variadic: true }];
  const capped: Port[] = [{ port: "items", variadic: true, max: 2 }];

  it("lets a single-value input take one wire and no more", () => {
    expect(inputHasRoom(single, "in", 0)).toBe(true);
    expect(inputHasRoom(single, "in", 1)).toBe(false);
  });

  it("caps a variadic input with no declared max at the default", () => {
    expect(inputHasRoom(variadic, "items", DEFAULT_MAX_VARIADIC_FAN_IN - 1)).toBe(true);
    expect(inputHasRoom(variadic, "items", DEFAULT_MAX_VARIADIC_FAN_IN)).toBe(false);
  });

  it("respects a variadic input's declared max", () => {
    expect(inputHasRoom(capped, "items", 1)).toBe(true);
    expect(inputHasRoom(capped, "items", 2)).toBe(false);
  });

  it("clamps a declared max at the absolute ceiling", () => {
    // A runner's or MCP host's manifest arrives over the wire and its max is
    // taken as given, so a drop cannot be allowed to raise its own ceiling.
    const unbounded: Port[] = [{ port: "items", variadic: true, max: 1_000_000 }];
    expect(inputHasRoom(unbounded, "items", MAX_VARIADIC_FAN_IN - 1)).toBe(true);
    expect(inputHasRoom(unbounded, "items", MAX_VARIADIC_FAN_IN)).toBe(false);
  });

  it("stays permissive for an undeclared pin", () => {
    expect(inputHasRoom(single, "mystery", 5)).toBe(true);
    expect(inputHasRoom(undefined, "in", 5)).toBe(true);
  });

  // A Reusable flow's pins are real ports named by its own params, each
  // carrying one value — the server refuses a second wire, so the canvas
  // must not draw one.
  it("holds an undeclared pin on a dynamic-ports drop to one wire", () => {
    expect(inputHasRoom(undefined, "mystery", 0, true)).toBe(true);
    expect(inputHasRoom(undefined, "mystery", 1, true)).toBe(false);
    expect(inputHasRoom(single, "mystery", 1, true)).toBe(false);
  });

  // A step this instance has no manifest for — a runner or MCP drop registered
  // elsewhere — gets the same treatment: the engine assembles one value per
  // port whether or not a manifest was available, and the server now refuses
  // the second wire, so the canvas must not draw it.
  it("holds a pin on a drop with no manifest to one wire", () => {
    expect(inputHasRoom(undefined, "in", 0, false, false)).toBe(true);
    expect(inputHasRoom(undefined, "in", 1, false, false)).toBe(false);
  });
});
