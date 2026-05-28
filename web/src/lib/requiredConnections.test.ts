import { describe, expect, it } from "vitest";
import {
  requiredConnections,
  requiredSecrets,
  unavailableProviders,
  unavailableSecretRefs,
} from "./requiredConnections";
import type { Manifest, OAuthProviderStatus } from "../types";

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
    const params = { q: { dsn: "${tenant:postgres_dsn}" } };
    expect(requiredSecrets(nodes, params, null)).toEqual([]);
  });

  it("flags a ${tenant:NAME} ref that isn't stored yet", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${tenant:postgres_dsn}", sql: "SELECT 1" } };
    expect(requiredSecrets(nodes, params, [])).toEqual(["postgres_dsn"]);
  });

  it("is satisfied when the secret exists", () => {
    const nodes = [node("q", "postgres_query")];
    const params = { q: { dsn: "${tenant:postgres_dsn}" } };
    expect(requiredSecrets(nodes, params, ["postgres_dsn"])).toEqual([]);
  });

  it("finds refs nested in arrays and objects", () => {
    const nodes = [node("h", "http_request")];
    const params = {
      h: {
        headers: { Authorization: "Bearer ${tenant:api_key}" },
        tags: ["x", "${tenant:other}"],
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
      read: { query: "after:${tenant:gmail_cursor}" },
      save: { name: "gmail_cursor", value: "123" },
    };
    // gmail_cursor is written by the secret_set node, so it's not "missing".
    expect(requiredSecrets(nodes, params, [])).toEqual([]);
  });

  it("dedupes a secret referenced by multiple nodes", () => {
    const nodes = [node("a", "postgres_query"), node("b", "postgres_upsert")];
    const params = {
      a: { dsn: "${tenant:postgres_dsn}" },
      b: { dsn: "${tenant:postgres_dsn}" },
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
    const params = { q: { dsn: "${tenant:postgres_dsn}" } };
    expect(unavailableSecretRefs(nodes, params, [])).toEqual([]);
  });

  it("names every ${tenant:NAME} ref when the secret store is off", () => {
    const nodes = [
      node("q", "postgres_query"),
      node("h", "http_request"),
    ];
    const params = {
      q: { dsn: "${tenant:postgres_dsn}" },
      h: { headers: { Authorization: "Bearer ${tenant:api_key}" } },
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
      read: { query: "after:${tenant:gmail_cursor}" },
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
