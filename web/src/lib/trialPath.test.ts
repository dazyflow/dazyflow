import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { Manifest } from "../types";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
} from "./requiredConnections";

// trialPath.test guards the "fresh user, no admin help" experience.
// Two things must stay true for the trial to convert:
//
//   1. The try-it-now template must remain forkable on day one —
//      no OAuth provider, no tenant secret. It's the one template
//      a non-technical persona can run before their admin enables
//      anything. If a future commit adds an `account` param or a
//      ${secret....} reference here, the test fails and the
//      no-setup promise is broken.
//
//   2. An OAuth-needing template — New email → Slack message — must
//      surface a clear "your administrator needs to enable Google
//      / Slack" signal when the install has no OAuth. The editor's
//      pre-run gate relies on this, so the run never silently
//      dispatches into a doomed setup. (This template references no
//      tenant secret — the poll/for_each/unwrap_results flow carries
//      everything it needs.)
//
// The fixtures are the actual JSON shipped under web/public — load
// them straight from disk so the test pins behaviour against what
// the gallery actually serves.

const REPO_ROOT = resolve(__dirname, "../../public/templates");

type TemplateGraph = {
  id: string;
  version?: string;
  name?: string;
  nodes?: Array<{
    id: string;
    module: string;
    params?: Record<string, unknown>;
  }>;
  edges?: unknown[];
  triggers?: unknown[];
};

function loadGraph(file: string): TemplateGraph {
  return JSON.parse(
    readFileSync(resolve(REPO_ROOT, file), "utf8"),
  ) as TemplateGraph;
}

// Stub manifest factory — only the fields requiredConnections /
// unavailableProviders actually read. We hand-wire the manifests for
// the drops the trial-path templates use; that's intentional, so
// the test doesn't drift the moment someone tweaks an unrelated
// manifest field.
function manifest(
  id: string,
  integration: string | undefined,
  hasAccountParam: boolean,
): Manifest {
  return {
    id,
    version: "1",
    label: id,
    integration,
    params_schema: hasAccountParam
      ? { type: "object", properties: { account: { type: "string" } } }
      : { type: "object", properties: {} },
  };
}

describe("try-it-now template — zero-setup trial path", () => {
  const tpl = loadGraph("try-it-now.json");

  it("is in the templates index marked no_setup", () => {
    const index = JSON.parse(
      readFileSync(resolve(REPO_ROOT, "index.json"), "utf8"),
    ) as {
      templates: Array<{
        id: string;
        no_setup?: boolean;
        integrations?: string[];
      }>;
    };
    const entry = index.templates.find((t) => t.id === "try-it-now");
    expect(entry, "try-it-now missing from index.json").toBeDefined();
    expect(entry!.no_setup).toBe(true);
    expect(entry!.integrations ?? []).toEqual([]);
  });

  it("references no OAuth provider in any node param", () => {
    // The two drops try-it-now uses are the json source + render_text —
    // neither is OAuth-backed. We hand-build their (minimal) manifests
    // with no `account` param.
    const manifestByID = new Map<string, Manifest>([
      ["json", manifest("json", undefined, false)],
      ["render_text", manifest("render_text", undefined, false)],
    ]);
    const nodes = (tpl.nodes ?? []).map((n) => ({
      id: n.id,
      data: { moduleID: n.module },
    }));
    const paramsByID = Object.fromEntries(
      (tpl.nodes ?? []).map((n) => [n.id, n.params ?? {}]),
    );

    // Even pretending OAuth is off: nothing in this template would
    // need a provider, so the unavailable-list comes back empty.
    expect(
      unavailableProviders(nodes, manifestByID, paramsByID, null),
    ).toEqual([]);
    // And with OAuth ON but no accounts connected, still nothing
    // missing — the template runs against the empty provider set.
    expect(
      requiredConnections(nodes, manifestByID, paramsByID, []),
    ).toEqual([]);
  });

  it("references no ${secret.NAME} credentials", () => {
    const paramsByID = Object.fromEntries(
      (tpl.nodes ?? []).map((n) => [n.id, n.params ?? {}]),
    );
    const nodes = (tpl.nodes ?? []).map((n) => ({
      id: n.id,
      data: { moduleID: n.module },
    }));

    // With the secret store ON and empty: no missing secrets.
    expect(requiredSecrets(nodes, paramsByID, [])).toEqual([]);
    // With the secret store OFF entirely: no admin-blocked secret
    // refs either (the template has none to need).
    expect(unavailableSecretRefs(nodes, paramsByID, null)).toEqual([]);

    // Defensive belt-and-braces: serialise every param value and
    // assert no ${secret. shows up anywhere in the JSON. Catches a
    // future commit that adds a ref inside an array/object the
    // recursive walker might miss.
    const flat = JSON.stringify(tpl.nodes ?? []);
    expect(flat).not.toContain("${secret.");
  });
});

describe("email-to-slack template — admin-blocked path", () => {
  const tpl = loadGraph("email-to-slack.json");

  // The graph uses poll_trigger, gmail_search_messages, for_each,
  // gmail_get_message, unwrap_results, render_text, slack_send_message.
  // Only the gmail + slack drops are OAuth-backed (and both take an
  // `account` param). The rest are pure transforms / triggers.
  const manifestByID = new Map<string, Manifest>([
    ["poll_trigger", manifest("poll_trigger", undefined, false)],
    ["gmail_search_messages", manifest("gmail_search_messages", "Gmail", true)],
    ["gmail_get_message", manifest("gmail_get_message", "Gmail", true)],
    ["for_each", manifest("for_each", undefined, false)],
    ["unwrap_results", manifest("unwrap_results", undefined, false)],
    ["render_text", manifest("render_text", undefined, false)],
    ["slack_send_message", manifest("slack_send_message", "Slack", true)],
  ]);

  function frame() {
    const nodes = (tpl.nodes ?? []).map((n) => ({
      id: n.id,
      data: { moduleID: n.module },
    }));
    const paramsByID = Object.fromEntries(
      (tpl.nodes ?? []).map((n) => [n.id, n.params ?? {}]),
    );
    return { nodes, paramsByID };
  }

  it("surfaces 'google' and 'slack' as admin-blocked when OAuth is off", () => {
    const { nodes, paramsByID } = frame();
    expect(
      unavailableProviders(nodes, manifestByID, paramsByID, null),
    ).toEqual(["google", "slack"]);
  });

  it("references no tenant secret, so nothing is admin-blocked on the secret axis", () => {
    const { nodes, paramsByID } = frame();
    // The flow carries everything through poll_trigger + for_each +
    // unwrap_results — no ${secret.…} reference anywhere. With the store
    // off there is therefore nothing to surface here; only the OAuth
    // providers (google/slack) are admin-blocked (asserted above).
    expect(unavailableSecretRefs(nodes, paramsByID, null)).toEqual([]);
  });

  it("when OAuth is on but no accounts are connected, both providers come back as missing", () => {
    const { nodes, paramsByID } = frame();
    const providers = [
      { name: "google", accounts: [] },
      { name: "slack", accounts: [] },
    ];
    const missing = requiredConnections(
      nodes,
      manifestByID,
      paramsByID,
      providers,
    );
    // Both drops use account="default" — the gate names each.
    expect(missing.map((m) => m.provider).sort()).toEqual(["google", "slack"]);
  });
});
