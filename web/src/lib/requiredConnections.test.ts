import { describe, expect, it } from "vitest";
import { requiredConnections } from "./requiredConnections";
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
