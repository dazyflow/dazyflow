// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { resolveOrgDeepLink, stripOrgParam, type Loc } from "./orgDeepLink";

const runLoc: Loc = { pathname: "/runs/run-1", search: "?org=acme", hash: "" };

describe("stripOrgParam", () => {
  it("removes the org param and the now-empty query", () => {
    expect(stripOrgParam(runLoc)).toBe("/runs/run-1");
  });

  it("keeps other params, their order, and the fragment", () => {
    expect(
      stripOrgParam({
        pathname: "/runs/run-1",
        search: "?tab=logs&org=acme&node=n2",
        hash: "#step-3",
      }),
    ).toBe("/runs/run-1?tab=logs&node=n2#step-3");
  });

  it("is a no-op when there is no org param", () => {
    expect(stripOrgParam({ pathname: "/runs/r", search: "?tab=logs", hash: "" })).toBe(
      "/runs/r?tab=logs",
    );
    expect(stripOrgParam({ pathname: "/overview", search: "", hash: "" })).toBe("/overview");
  });
});

describe("resolveOrgDeepLink", () => {
  it("does nothing without an org param", () => {
    expect(
      resolveOrgDeepLink({
        requested: "",
        available: ["acme", "globex"],
        sessionTenant: "globex",
        loc: { pathname: "/runs/r", search: "", hash: "" },
      }),
    ).toEqual({ kind: "none" });
  });

  // A link naming an org the user isn't in (revoked membership, a forwarded
  // mail, a hand-edited URL) must not disturb the session they do have.
  it("ignores an org the user cannot act in", () => {
    expect(
      resolveOrgDeepLink({
        requested: "initech",
        available: ["acme", "globex"],
        sessionTenant: "globex",
        loc: runLoc,
      }),
    ).toEqual({ kind: "none" });
  });

  it("switches when the session is scoped to another org", () => {
    expect(
      resolveOrgDeepLink({
        requested: "acme",
        available: ["acme", "globex"],
        sessionTenant: "globex",
        loc: runLoc,
      }),
    ).toEqual({ kind: "switch", tenant: "acme", url: "/runs/run-1" });
  });

  // The common case once the user is already in the right org: no reload, just
  // drop the param so it can't re-assert itself later.
  it("adopts without a reload when the session already matches", () => {
    expect(
      resolveOrgDeepLink({
        requested: "acme",
        available: ["acme", "globex"],
        sessionTenant: "acme",
        loc: runLoc,
      }),
    ).toEqual({ kind: "adopt", tenant: "acme", url: "/runs/run-1" });
  });

  it("tolerates whitespace around the param value", () => {
    expect(
      resolveOrgDeepLink({
        requested: "  acme  ",
        available: ["acme"],
        sessionTenant: "globex",
        loc: runLoc,
      }),
    ).toEqual({ kind: "switch", tenant: "acme", url: "/runs/run-1" });
  });

  // A single-org user's link is still worth adopting: it costs nothing and
  // keeps the URL clean.
  it("adopts for a single-org user", () => {
    expect(
      resolveOrgDeepLink({
        requested: "acme",
        available: ["acme"],
        sessionTenant: "acme",
        loc: runLoc,
      }),
    ).toEqual({ kind: "adopt", tenant: "acme", url: "/runs/run-1" });
  });

  // Nothing here is run-specific: the same param resolves a support ticket, a
  // flow, or any other org-scoped route the server pins.
  it("is route-agnostic", () => {
    expect(
      resolveOrgDeepLink({
        requested: "acme",
        available: ["acme", "globex"],
        sessionTenant: "globex",
        loc: { pathname: "/support/abc123", search: "?org=acme", hash: "" },
      }),
    ).toEqual({ kind: "switch", tenant: "acme", url: "/support/abc123" });
  });

  it("carries the rest of the deep link through a switch", () => {
    expect(
      resolveOrgDeepLink({
        requested: "acme",
        available: ["acme", "globex"],
        sessionTenant: "globex",
        loc: { pathname: "/runs/run-1", search: "?org=acme&tab=logs", hash: "#n2" },
      }),
    ).toEqual({ kind: "switch", tenant: "acme", url: "/runs/run-1?tab=logs#n2" });
  });
});
