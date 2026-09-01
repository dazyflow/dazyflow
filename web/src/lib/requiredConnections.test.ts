// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
  unavailableConnectionApps,
  nodeSetupNeeded,
  missingConnectionApps,
  setupDestination,
} from "./requiredConnections";
import type { ConnectionField, Manifest, OAuthProviderStatus } from "../types";

// claudeManifest mirrors the ConnectionFields shape (Claude / ntfy / SMTP):
// no OAuth, no ${secret} ref — credentials live at conn.<slug>.<key>.
function fieldsManifest(
  id: string,
  integration: string,
  fields: ConnectionField[],
): Manifest {
  return {
    id,
    version: "1",
    label: id,
    integration,
    params_schema: { type: "object", properties: { api_key: { type: "string" } } },
    connection_fields: fields,
  };
}

// Minimal manifest factory — only the fields requiredConnections reads.
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

function manifestMap(...m: Manifest[]): Map<string, Manifest> {
  return new Map(m.map((x) => [x.id, x]));
}

const node = (id: string, moduleID: string) => ({ id, data: { moduleID } });

describe("requiredConnections", () => {
  it("returns [] when providers is null (OAuth disabled / unknown)", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    expect(requiredConnections(nodes, mm, {}, null)).toEqual([]);
  });

  it("flags an OAuth drop whose account isn't connected", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const providers: OAuthProviderStatus[] = [{ name: "slack", accounts: [] }];
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([
      { provider: "slack", account: "default" },
    ]);
  });

  it("is satisfied when the required account is connected", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const providers: OAuthProviderStatus[] = [
      { name: "slack", accounts: ["default"] },
    ];
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([]);
  });

  it("respects a non-default account param", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const params = { n1: { account: "marketing" } };
    const providers: OAuthProviderStatus[] = [
      { name: "slack", accounts: ["default"] },
    ];
    expect(requiredConnections(nodes, mm, params, providers)).toEqual([
      { provider: "slack", account: "marketing" },
    ]);
  });

  it("maps Gmail and Google Sheets to the shared 'google' provider", () => {
    const nodes = [node("a", "gmail_send_email"), node("b", "sheets_append_row")];
    const mm = manifestMap(
      manifest("gmail_send_email", "Gmail", true),
      manifest("sheets_append_row", "Google Sheets", true),
    );
    const providers: OAuthProviderStatus[] = [{ name: "google", accounts: [] }];
    // Both need google/default — deduped to a single entry.
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([
      { provider: "google", account: "default" },
    ]);
  });

  it("ignores drops without an account param (e.g. webhook triggers)", () => {
    const nodes = [node("n1", "slack_on_mention")];
    const mm = manifestMap(manifest("slack_on_mention", "Slack", false));
    const providers: OAuthProviderStatus[] = [{ name: "slack", accounts: [] }];
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([]);
  });

  it("ignores non-OAuth integrations (e.g. Postgres)", () => {
    const nodes = [node("n1", "postgres_query")];
    const mm = manifestMap(manifest("postgres_query", "Postgres", true));
    const providers: OAuthProviderStatus[] = [];
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([]);
  });

  it("skips the lookup when a raw token param is supplied", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const params = { n1: { token: "xoxb-raw" } };
    const providers: OAuthProviderStatus[] = [{ name: "slack", accounts: [] }];
    expect(requiredConnections(nodes, mm, params, providers)).toEqual([]);
  });

  it("dedupes multiple nodes needing the same provider/account", () => {
    const nodes = [
      node("a", "slack_send_message"),
      node("b", "slack_send_message"),
    ];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const providers: OAuthProviderStatus[] = [{ name: "slack", accounts: [] }];
    expect(requiredConnections(nodes, mm, {}, providers)).toEqual([
      { provider: "slack", account: "default" },
    ]);
  });
});

describe("requiredSecrets", () => {
  it("returns [] when knownSecrets is null (store disabled / no permission)", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${secret.postgres_dsn}" } };
    expect(requiredSecrets(nodes, params, null)).toEqual([]);
  });

  it("flags a ${secret.NAME} ref that isn't stored yet", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${secret.postgres_dsn}", sql: "SELECT 1" } };
    expect(requiredSecrets(nodes, params, [])).toEqual(["postgres_dsn"]);
  });

  it("is satisfied when the secret exists", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${secret.postgres_dsn}" } };
    expect(requiredSecrets(nodes, params, ["postgres_dsn"])).toEqual([]);
  });

  it("finds refs nested in arrays and objects", () => {
    const nodes = [node("h", "http_request")];
    const params = {
      h: {
        headers: { Authorization: "Bearer ${secret.api_key}" },
        tags: ["x", "${secret.other}"],
      },
    };
    expect(requiredSecrets(nodes, params, [])).toEqual(["api_key", "other"]);
  });

  it("excludes secrets the graph writes itself via secret_set", () => {
    const nodes = [
      node("read", "gmail_search_messages"),
      node("save", "secret_set"),
    ];
    const params = {
      read: { query: "after:${secret.gmail_cursor}" },
      save: { name: "gmail_cursor", value: "123" },
    };
    // gmail_cursor is written by the secret_set node, so it's not "missing".
    expect(requiredSecrets(nodes, params, [])).toEqual([]);
  });

  it("dedupes a secret referenced by multiple nodes", () => {
    const nodes = [node("a", "postgres_query"), node("b", "postgres_upsert")];
    const params = {
      a: { dsn: "${secret.postgres_dsn}" },
      b: { dsn: "${secret.postgres_dsn}" },
    };
    expect(requiredSecrets(nodes, params, [])).toEqual(["postgres_dsn"]);
  });
});

describe("unavailableProviders", () => {
  it("returns [] when providers is not null (regular path applies)", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    expect(unavailableProviders(nodes, mm, {}, [])).toEqual([]);
  });

  it("names every OAuth provider the graph would need when OAuth is off", () => {
    const nodes = [
      node("a", "slack_send_message"),
      node("b", "gmail_send_email"),
    ];
    const mm = manifestMap(
      manifest("slack_send_message", "Slack", true),
      manifest("gmail_send_email", "Gmail", true),
    );
    expect(unavailableProviders(nodes, mm, {}, null)).toEqual([
      "google",
      "slack",
    ]);
  });

  it("dedupes Gmail + Sheets to the shared 'google' provider", () => {
    const nodes = [
      node("a", "gmail_send_email"),
      node("b", "sheets_append_row"),
    ];
    const mm = manifestMap(
      manifest("gmail_send_email", "Gmail", true),
      manifest("sheets_append_row", "Google Sheets", true),
    );
    expect(unavailableProviders(nodes, mm, {}, null)).toEqual(["google"]);
  });

  it("ignores nodes whose drop is not OAuth-backed", () => {
    const nodes = [node("n1", "postgres_query")];
    const mm = manifestMap(manifest("postgres_query", "Postgres", true));
    expect(unavailableProviders(nodes, mm, {}, null)).toEqual([]);
  });

  it("ignores OAuth drops without an account param", () => {
    const nodes = [node("n1", "slack_on_mention")];
    const mm = manifestMap(manifest("slack_on_mention", "Slack", false));
    expect(unavailableProviders(nodes, mm, {}, null)).toEqual([]);
  });

  it("ignores nodes that supply a raw token", () => {
    const nodes = [node("n1", "slack_send_message")];
    const mm = manifestMap(manifest("slack_send_message", "Slack", true));
    const params = { n1: { token: "xoxb-raw" } };
    expect(unavailableProviders(nodes, mm, params, null)).toEqual([]);
  });

  it("returns [] when the graph references no OAuth-backed drops", () => {
    const nodes = [node("n1", "postgres_query")];
    const mm = manifestMap(manifest("postgres_query", "Postgres", true));
    expect(unavailableProviders(nodes, mm, {}, null)).toEqual([]);
  });
});

describe("unavailableSecretRefs", () => {
  it("returns [] when knownSecrets is not null (regular path applies)", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${secret.postgres_dsn}" } };
    expect(unavailableSecretRefs(nodes, params, [])).toEqual([]);
  });

  it("names every ${secret.NAME} ref when the secret store is off", () => {
    const nodes = [
      node("q", "postgres_query"),
      node("h", "http_request"),
    ];
    const params = {
      q: { dsn: "${secret.postgres_dsn}" },
      h: { headers: { Authorization: "Bearer ${secret.api_key}" } },
    };
    expect(unavailableSecretRefs(nodes, params, null)).toEqual([
      "api_key",
      "postgres_dsn",
    ]);
  });

  it("excludes secrets the graph writes itself via secret_set", () => {
    const nodes = [
      node("read", "gmail_search_messages"),
      node("save", "secret_set"),
    ];
    const params = {
      read: { query: "after:${secret.gmail_cursor}" },
      save: { name: "gmail_cursor", value: "123" },
    };
    expect(unavailableSecretRefs(nodes, params, null)).toEqual([]);
  });

  it("returns [] when the graph references no tenant secrets", () => {
    const nodes = [node("n1", "compute_rows")];
    const params = { n1: { compute: { x: "1+1" } } };
    expect(unavailableSecretRefs(nodes, params, null)).toEqual([]);
  });
});

describe("nodeSetupNeeded", () => {
  const apiKeyField: ConnectionField = { key: "api_key", label: "API key", required: true };

  it("flags a ConnectionFields drop when the connection isn't configured", () => {
    const m = fieldsManifest("ai_summarize", "Claude", [apiKeyField]);
    expect(nodeSetupNeeded(m, {}, [], [])).toEqual({
      integration: "Claude",
      slug: "claude",
    });
  });

  it("does NOT flag an app whose fields are all optional (e.g. ntfy)", () => {
    // ntfy: server + token, neither required → runs out of the box.
    const m = fieldsManifest("ntfy", "ntfy", [
      { key: "server", label: "Server URL" },
      { key: "token", label: "Access token", secret: true },
    ]);
    expect(nodeSetupNeeded(m, {}, [], [])).toBeNull();
  });

  it("is satisfied when conn.<slug>.<key> secret exists", () => {
    const m = fieldsManifest("ai_summarize", "Claude", [apiKeyField]);
    expect(nodeSetupNeeded(m, {}, [], ["conn.claude.api_key"])).toBeNull();
  });

  it("is satisfied when the key is pasted inline on the node", () => {
    const m = fieldsManifest("ai_summarize", "Claude", [apiKeyField]);
    expect(nodeSetupNeeded(m, { api_key: "sk-ant-x" }, [], [])).toBeNull();
  });

  it("returns null when secrets is null (store off / unknown — don't nag)", () => {
    const m = fieldsManifest("ai_summarize", "Claude", [apiKeyField]);
    expect(nodeSetupNeeded(m, {}, [], null)).toBeNull();
  });

  it("flags an OAuth drop whose account isn't connected", () => {
    const m = manifest("slack_send_message", "Slack", true);
    expect(nodeSetupNeeded(m, {}, [{ name: "slack", accounts: [] }], [])).toEqual({
      integration: "Slack",
      slug: "slack",
    });
  });
});

describe("missingConnectionApps", () => {
  const apiKeyField: ConnectionField = { key: "api_key", label: "API key", required: true };

  it("dedupes apps across nodes and ignores configured ones", () => {
    const nodes = [
      node("n1", "ai_summarize"),
      node("n2", "ai_classify"),
      node("n3", "ai_extract"),
    ];
    const mm = manifestMap(
      fieldsManifest("ai_summarize", "Claude", [apiKeyField]),
      fieldsManifest("ai_classify", "Claude", [apiKeyField]),
      fieldsManifest("ai_extract", "Claude", [apiKeyField]),
    );
    // None configured → one deduped Claude entry.
    expect(missingConnectionApps(nodes, mm, {}, [])).toEqual([
      { integration: "Claude", slug: "claude" },
    ]);
    // Connection present → nothing missing.
    expect(missingConnectionApps(nodes, mm, {}, ["conn.claude.api_key"])).toEqual([]);
  });
});

// The pre-run gate's primary button used to say "Go to Connections" in every
// case — a page this product doesn't have — and always navigated to /apps,
// which is a dead end when what's missing is a secret: nothing on the Apps page
// adds one. Target and label are derived together here so they can't drift.
describe("setupDestination", () => {
  it("deep-links the one app that needs connecting", () => {
    expect(setupDestination(["fortnox"], [], true)).toEqual({
      to: "/apps/fortnox",
      labelKey: "connGate.connect",
    });
    // Repeats of the same app are still one app.
    expect(setupDestination(["slack", "slack"], [], true).to).toBe("/apps/slack");
  });

  it("falls back to the Apps list for several apps", () => {
    expect(setupDestination(["slack", "gmail"], [], true)).toEqual({
      to: "/apps",
      labelKey: "connGate.connect",
    });
  });

  it("sends a secrets-only gap to the secret store, focused when there's one", () => {
    expect(setupDestination([], ["FORTNOX_TOKEN"], true)).toEqual({
      to: "/admin/secrets?focus=FORTNOX_TOKEN",
      labelKey: "connGate.connectSecrets",
    });
    // Several secrets: the store, unfocused — there's no one row to highlight.
    expect(setupDestination([], ["A", "B"], true)).toEqual({
      to: "/admin/secrets",
      labelKey: "connGate.connectSecrets",
    });
  });

  it("escapes a secret name that isn't URL-safe", () => {
    expect(setupDestination([], ["MY TOKEN/v2"], true).to).toBe(
      "/admin/secrets?focus=MY%20TOKEN%2Fv2",
    );
  });

  it("prefers the app page when both an app and a secret are missing", () => {
    // Connecting the app is the bigger, more likely blocker, and the Apps list
    // is the honest destination when one page can't fix everything.
    expect(setupDestination(["slack"], ["TOKEN"], true)).toEqual({
      to: "/apps",
      labelKey: "connGate.connect",
    });
  });

  it("defaults to the Apps list when nothing is user-fixable", () => {
    // Everything is admin-blocked: the button isn't the way out, so don't
    // promise a deep link that fixes it.
    expect(setupDestination(["slack"], [], false)).toEqual({
      to: "/apps",
      labelKey: "connGate.connect",
    });
    expect(setupDestination([], [], false).to).toBe("/apps");
  });
});

// With DAZYFLOW_MASTER_KEY empty — the quick start's default — the secret
// store is off and listSecrets fails, so the editor holds secrets === null.
// The conn.<slug>.<key> apps then warned about nothing at all: no pre-run
// banner, no node badge. OAuth and ${secret.NAME} already had this parallel.
describe("unavailableConnectionApps", () => {
  const stripe = fieldsManifest("stripe_get_customer", "Stripe", [
    { key: "api_key", label: "Secret API key", secret: true, required: true },
  ]);

  it("returns [] when the store is on (the regular check applies)", () => {
    const nodes = [node("n1", "stripe_get_customer")];
    expect(unavailableConnectionApps(nodes, manifestMap(stripe), {}, [])).toEqual([]);
  });

  it("names the app when the store is off and nothing is filled in", () => {
    const nodes = [node("n1", "stripe_get_customer")];
    const out = unavailableConnectionApps(nodes, manifestMap(stripe), {}, null);
    expect(out.map((n) => n.slug)).toEqual(["stripe"]);
  });

  it("stays quiet when the required field is typed straight into the node", () => {
    const nodes = [node("n1", "stripe_get_customer")];
    const params = { n1: { api_key: "sk_test_123" } };
    expect(unavailableConnectionApps(nodes, manifestMap(stripe), params, null)).toEqual([]);
  });

  it("dedupes several nodes of the same app", () => {
    const nodes = [node("a", "stripe_get_customer"), node("b", "stripe_get_customer")];
    const out = unavailableConnectionApps(nodes, manifestMap(stripe), {}, null);
    expect(out).toHaveLength(1);
  });

  it("ignores apps whose fields are all optional", () => {
    const ntfy = fieldsManifest("ntfy_publish", "ntfy", [
      { key: "server", label: "Server", required: false },
    ]);
    const nodes = [node("n1", "ntfy_publish")];
    expect(unavailableConnectionApps(nodes, manifestMap(ntfy), {}, null)).toEqual([]);
  });
});
