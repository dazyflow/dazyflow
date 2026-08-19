// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { expandToken, scoreDrop } from "./dropSearch";
import type { Manifest } from "../types";

// A slice of the real catalog — labels, subtitles, integrations and tags
// copied verbatim from the live /drops response, so a passing test means the
// alias actually lands on the shipped manifest text.
function drop(
  id: string,
  label: string,
  subtitle: string,
  integration: string,
  tags: string,
): Manifest {
  return {
    id,
    version: "1",
    label,
    subtitle: subtitle || undefined,
    integration: integration || undefined,
    tags: tags ? tags.split(",") : undefined,
  } as Manifest;
}

const CATALOG: Manifest[] = [
  drop("email_send", "Email", "Send email", "Email", "email,smtp,notify,report"),
  drop("gmail_send_email", "Gmail", "Send email", "Gmail", "gmail,email,send,smtp"),
  drop("gmail_search_messages", "Gmail", "Search emails", "Gmail", "gmail,email,search,list"),
  drop("slack_send_message", "Slack", "Send message", "Slack", "slack,chat,notify,send"),
  drop("ntfy", "ntfy", "Send notification", "ntfy",
    "ntfy,push,notify,notification,alert,phone,reminder,message,ping"),
  drop("cron_trigger", "Schedule", "", "", "cron,schedule,trigger,daily,recurring,timer"),
  drop("poll_trigger", "Interval", "", "", "poll,trigger,interval,schedule"),
  drop("webhook_input", "Webhook", "", "", "webhook,trigger,http,event"),
  drop("sheets_read_range", "Google Sheets", "Read range", "Google Sheets",
    "sheets,google,read,spreadsheet"),
  drop("excel_read", "Excel", "Read sheet", "Excel", "excel,xlsx,spreadsheet,read"),
  drop("fortnox_create_invoice", "Fortnox", "Create invoice", "Fortnox",
    "fortnox,invoice,accounting,invoicing,sweden"),
  drop("fortnox_create_customer", "Fortnox", "Create customer", "Fortnox",
    "fortnox,customer,accounting,invoicing,sweden"),
  drop("stripe_create_customer", "Stripe", "Create customer", "Stripe",
    "stripe,customer,billing,payments"),
  drop("klarna_get_order", "Klarna", "Get order", "Klarna",
    "klarna,order,payment,bnpl,checkout,sweden,nordic"),
  drop("nshift_create_shipment", "nShift", "Create shipment", "nShift",
    "nshift,unifaun,consignor,shipping,logistics,parcel,carrier,sweden,nordic"),
  drop("roaring_company_overview", "Roaring", "Company overview", "Roaring",
    "roaring,company,enrichment,org-number,orgnr,kyc,business,sweden,nordic"),
  drop("smhi_current", "SMHI Weather", "Current conditions", "SMHI",
    "weather,smhi,forecast,temperature,coordinate,lat,lon,current,conditions,sweden,nordic,no key"),
  drop("gcal_create_event", "Google Calendar", "Create event", "Google Calendar",
    "calendar,google,events,create"),
  drop("await_approval", "Wait for approval", "", "",
    "human_in_the_loop,approval,pause,wait"),
  drop("render_text", "Make text", "Text from a list", "",
    "transform,text,render,format,join,reduce,message,notify"),
  drop("sort_rows", "Sort rows", "", "", "transform,sort,order,etl"),
  drop("group_aggregate", "Group & summarize", "", "",
    "transform,group,aggregate,pivot,sum,etl,sql"),
  drop("postgres_query", "Postgres", "Query", "Postgres",
    "postgres,postgresql,sql,database,query,select"),
  drop("rss", "RSS / Atom feed", "New items from a feed", "",
    "rss,atom,feed,trigger,poll,news,syndication,subscribe"),
  drop("homeassistant_call_service", "Home Assistant", "Call service", "Home Assistant",
    "home assistant,homeassistant,hass,smart home,iot,light,switch,scene,service"),
  drop("elks_send_sms", "46elks", "Send SMS", "46elks",
    "46elks,elks,sms,text,message,notify,sweden,nordic"),
  drop("claude_summarize", "Claude", "Summarize", "Claude",
    "ai,claude,summary,summarize,text,tldr"),
];

// ranked returns the matching drop ids, best first — the order the palette
// renders.
function ranked(query: string): string[] {
  return CATALOG.map((d) => ({ id: d.id, s: scoreDrop(d, query) }))
    .filter((r) => r.s > 0)
    .sort((a, b) => b.s - a.s || a.id.localeCompare(b.id))
    .map((r) => r.id);
}

describe("Swedish queries reach the English catalog", () => {
  // The Marina walkthrough's three dead ends, which returned 0 hits.
  it.each([
    ["schema", "cron_trigger"],
    ["e-post", "email_send"],
    ["mejl", "email_send"],
  ])("%s finds %s", (query, expected) => {
    expect(ranked(query)[0]).toBe(expected);
  });

  it.each([
    ["kalkylblad", ["sheets_read_range", "excel_read"]],
    ["faktura", ["fortnox_create_invoice"]],
    ["kund", ["fortnox_create_customer", "stripe_create_customer"]],
    ["betalning", ["klarna_get_order"]],
    ["frakt", ["nshift_create_shipment"]],
    ["paket", ["nshift_create_shipment"]],
    ["orgnummer", ["roaring_company_overview"]],
    ["väder", ["smhi_current"]],
    ["kalender", ["gcal_create_event"]],
    ["påminnelse", ["ntfy"]],
    ["godkännande", ["await_approval"]],
    ["sortera", ["sort_rows"]],
    ["summera", ["group_aggregate"]],
    ["databas", ["postgres_query"]],
    ["nyheter", ["rss"]],
    ["lampa", ["homeassistant_call_service"]],
    ["sms", ["elks_send_sms"]],
    ["sammanfatta", ["claude_summarize"]],
    ["formulär", ["webhook_input"]],
    ["intervall", ["poll_trigger"]],
    ["mall", ["render_text"]],
  ])("%s surfaces %j", (query, expected) => {
    const hits = ranked(query);
    for (const id of expected as string[]) expect(hits).toContain(id);
  });

  it("folds Swedish vowels so an ASCII spelling works too", () => {
    expect(ranked("vader")).toEqual(ranked("väder"));
    expect(ranked("paminnelse")).toEqual(ranked("påminnelse"));
  });

  it("handles inflected forms via the ending stripper", () => {
    expect(ranked("fakturor")).toContain("fortnox_create_invoice");
    expect(ranked("kunder")).toContain("fortnox_create_customer");
    expect(ranked("notiser")).toContain("ntfy");
    expect(ranked("betalningar")).toContain("klarna_get_order");
  });

  it("expands a prefix while the user is still typing", () => {
    expect(ranked("fakt")).toContain("fortnox_create_invoice");
    expect(ranked("kalkyl")).toContain("sheets_read_range");
  });

  it("reads a Swedish compound through its head word", () => {
    expect(ranked("fakturamall")).toContain("fortnox_create_invoice");
    expect(ranked("telefonnummer")).toContain("elks_send_sms");
  });

  it("matches every token in a multi-word Swedish query", () => {
    // Both send-email drops satisfy "skicka mejl"; either may lead (Email's
    // label is an exact hit on the alias, Gmail's is a tag hit), so the
    // contract is that they take the top two rows.
    expect(ranked("skicka mejl").slice(0, 2).sort()).toEqual([
      "email_send",
      "gmail_send_email",
    ]);
    expect(ranked("skicka sms")).toContain("elks_send_sms");
  });
});

describe("English ranking is unchanged", () => {
  it("keeps an exact label hit at the top", () => {
    expect(ranked("slack")[0]).toBe("slack_send_message");
    expect(ranked("ntfy")[0]).toBe("ntfy");
    expect(ranked("excel")[0]).toBe("excel_read");
  });

  it("ranks the send-email drops for the English phrase", () => {
    expect(ranked("send email").slice(0, 2).sort()).toEqual([
      "email_send",
      "gmail_send_email",
    ]);
  });

  it("scores an alias hit strictly below the literal hit it mimics", () => {
    const n = CATALOG.find((d) => d.id === "ntfy")!;
    expect(scoreDrop(n, "notis")).toBeLessThan(scoreDrop(n, "ntfy"));
    // Compare like with like: the invariant is per-term (an alias hit is
    // 0.7x the same term typed literally), not across terms — "faktura"
    // legitimately beats a literal "invoice" here because it also expands to
    // the brand "fortnox", which is an exact label hit. That is the intended
    // Sweden-first behaviour, not a ranking bug.
    const f = CATALOG.find((d) => d.id === "fortnox_create_invoice")!;
    expect(scoreDrop(f, "faktura")).toBeLessThan(scoreDrop(f, "fortnox"));
  });

  it("still rejects a query nothing matches", () => {
    expect(ranked("xyzzy")).toEqual([]);
    expect(ranked("slack xyzzy")).toEqual([]);
  });

  it("returns every drop for an empty query", () => {
    expect(ranked("")).toHaveLength(CATALOG.length);
    expect(ranked("   ")).toHaveLength(CATALOG.length);
  });
});

describe("searching the localized names", () => {
  // The palette passes the text the reader SEES; a Swedish user types what is
  // on the row, which may not be in the alias table at all.
  const sv = (id: string, label: string, subtitle = "") => {
    const d = CATALOG.find((x) => x.id === id)!;
    return { drop: d, localized: { label, subtitle } };
  };

  it("matches a translated label the alias table never mentions", () => {
    const { drop: d, localized } = sv("await_approval", "Vänta på godkännande");
    expect(scoreDrop(d, "vänta", localized)).toBeGreaterThan(0);
    // Same query without the localized text only lands via the alias table, so
    // this is really testing the localized surface.
    expect(scoreDrop(d, "vänta på godkännande", localized)).toBeGreaterThan(
      scoreDrop(d, "vänta på godkännande"),
    );
  });

  it("matches a translated subtitle", () => {
    const { drop: d, localized } = sv(
      "fortnox_create_invoice",
      "Fortnox",
      "Skapa faktura",
    );
    expect(scoreDrop(d, "skapa", localized)).toBeGreaterThan(0);
    expect(scoreDrop(d, "skapa faktura", localized)).toBeGreaterThan(0);
  });

  it("keeps the English name searchable in a Swedish UI", () => {
    const { drop: d, localized } = sv("email_send", "E-post", "Skicka e-post");
    expect(scoreDrop(d, "email", localized)).toBe(scoreDrop(d, "email"));
    expect(scoreDrop(d, "send email", localized)).toBe(
      scoreDrop(d, "send email"),
    );
  });

  it("ignores a localized value identical to the English one", () => {
    const { drop: d, localized } = sv("slack_send_message", "Slack", "Send message");
    expect(scoreDrop(d, "slack", localized)).toBe(scoreDrop(d, "slack"));
  });
});

describe("expandToken", () => {
  it("has no Swedish reading for English catalog words", () => {
    expect(expandToken("slack")).toEqual([]);
    expect(expandToken("webhook")).toEqual([]);
  });

  it("expands a Swedish word to terms that exist in the catalog", () => {
    expect(expandToken("mejl")).toContain("email");
    expect(expandToken("frakt")).toContain("shipping");
  });

  it("ignores a token too short to expand safely", () => {
    expect(expandToken("or")).toEqual([]);
  });
});
