import { describe, expect, it } from "vitest";
import { explainRunError } from "./explainRunError";

describe("explainRunError", () => {
  it("returns null for empty input", () => {
    expect(explainRunError(undefined, undefined)).toBeNull();
    expect(explainRunError("", "")).toBeNull();
  });

  it("returns null for an unrecognised message", () => {
    expect(explainRunError("", "Something exploded in the warp core")).toBeNull();
  });

  it("matches the OAuth-account-not-connected pattern (Slack)", () => {
    const r = explainRunError(
      "",
      'slack account "marketing" is not connected',
    );
    expect(r).not.toBeNull();
    expect(r!.headlineKey).toBe("explain.oauthAccountMissing");
    expect(r!.headlineValues).toEqual({
      provider: "Slack",
      account: "marketing",
    });
    expect(r!.action?.href).toBe("/integrations");
  });

  it("matches the OAuth-account-not-connected pattern (Google)", () => {
    const r = explainRunError(
      "",
      'google account "sarah@coffee.example" is not connected',
    );
    expect(r!.headlineValues).toEqual({
      provider: "Google",
      account: "sarah@coffee.example",
    });
  });

  it("matches 'no Slack token' shape (provider not connected at all)", () => {
    const r = explainRunError(
      "",
      "no Slack token: pass `token` directly or connect a Slack account via /api/v1/oauth/slack/authorize",
    );
    expect(r!.headlineKey).toBe("explain.providerNotConnected");
    expect(r!.headlineValues).toEqual({ provider: "Slack" });
    expect(r!.action?.href).toBe("/integrations");
  });

  it("matches Slack channel_not_found", () => {
    const r = explainRunError(
      "slack_error",
      "Slack rejected message: channel_not_found",
    );
    expect(r!.headlineKey).toBe("explain.slackChannelNotFound");
    expect(r!.action?.labelKey).toBe("explain.actionInviteBot");
  });

  it("matches Slack not_in_channel", () => {
    const r = explainRunError(
      "slack_error",
      "Slack rejected message: not_in_channel",
    );
    expect(r!.headlineKey).toBe("explain.slackNotInChannel");
  });

  it("matches Slack invalid_auth → reconnect", () => {
    const r = explainRunError(
      "slack_error",
      "Slack rejected message: invalid_auth",
    );
    expect(r!.headlineKey).toBe("explain.slackInvalidAuth");
    expect(r!.action?.href).toBe("/integrations");
  });

  it("matches a missing tenant secret with double quotes", () => {
    const r = explainRunError("", 'secret "postgres_dsn" not found');
    expect(r!.headlineKey).toBe("explain.credentialMissing");
    expect(r!.headlineValues).toEqual({ name: "postgres_dsn" });
    expect(r!.action?.href).toBe("/secrets?focus=postgres_dsn");
  });

  it("matches the builtin-secret variant", () => {
    const r = explainRunError("", 'builtin secret "API_KEY" not found');
    expect(r!.headlineValues).toEqual({ name: "API_KEY" });
  });

  it("encodes special characters in the focus query param", () => {
    const r = explainRunError("", 'secret "my secret" not found');
    expect(r!.action?.href).toBe("/secrets?focus=my%20secret");
  });
});
