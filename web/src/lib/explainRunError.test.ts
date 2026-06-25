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
    expect(r!.action?.href).toBe("/apps");
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
    expect(r!.action?.href).toBe("/apps");
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
    expect(r!.action?.href).toBe("/apps");
  });

  it("matches a missing tenant secret with double quotes", () => {
    const r = explainRunError("", 'secret "postgres_dsn" not found');
    expect(r!.headlineKey).toBe("explain.secretMissing");
    expect(r!.headlineValues).toEqual({ name: "postgres_dsn" });
    expect(r!.action?.href).toBe("/admin/secrets?focus=postgres_dsn");
  });

  it("matches the builtin-secret variant", () => {
    const r = explainRunError("", 'builtin secret "API_KEY" not found');
    expect(r!.headlineValues).toEqual({ name: "API_KEY" });
  });

  it("encodes special characters in the focus query param", () => {
    const r = explainRunError("", 'secret "my secret" not found');
    expect(r!.action?.href).toBe("/admin/secrets?focus=my%20secret");
  });

  it("maps structured infra/runtime codes to plain-English headlines", () => {
    const cases: Record<string, string> = {
      egress_blocked: "explain.egressBlocked",
      ssrf_blocked: "explain.ssrfBlocked",
      sandbox_escape: "explain.sandboxEscape",
      too_large: "explain.tooLarge",
      body_too_large: "explain.tooLarge",
      quota_exceeded: "explain.quotaExceeded",
      timeout: "explain.timeout",
      cancelled: "explain.cancelled",
      missing_input: "explain.missingInput",
      bad_input: "explain.badInput",
      bad_param: "explain.badParam",
      eval: "explain.evalFailed",
      unknown_step: "explain.unknownStep",
      db: "explain.dbFailed",
    };
    for (const [code, headlineKey] of Object.entries(cases)) {
      const r = explainRunError(code, "some low-level go error");
      expect(r, code).not.toBeNull();
      expect(r!.headlineKey, code).toBe(headlineKey);
      // Infra codes are headline-only — no misleading fix-it button.
      expect(r!.action, code).toBeUndefined();
    }
  });

  it("maps 5xx / gateway errors to the transient service-unavailable headline", () => {
    for (const msg of [
      "twilio returned 503: Service Unavailable",
      "502 Bad Gateway",
      "upstream: gateway timeout",
    ]) {
      const r = explainRunError("http", msg);
      expect(r, msg).not.toBeNull();
      expect(r!.headlineKey, msg).toBe("explain.serviceUnavailable");
      expect(r!.action, msg).toBeUndefined(); // transient — no fix-it button
    }
  });

  it("matches email-delivery rejections", () => {
    for (const msg of [
      "smtp: 550 5.1.1 mailbox unavailable",
      "send email: 552 message too large",
      "relay access denied for recipient",
    ]) {
      const r = explainRunError("", msg);
      expect(r, msg).not.toBeNull();
      expect(r!.headlineKey, msg).toBe("explain.emailSendFailed");
    }
  });

  it("matches remote input-validation rejections", () => {
    for (const msg of [
      '"email" is required',
      "field address cannot be empty",
      "400 Bad Request: validation failed",
      "422 Unprocessable Entity",
    ]) {
      const r = explainRunError("", msg);
      expect(r, msg).not.toBeNull();
      expect(r!.headlineKey, msg).toBe("explain.remoteRejectedInput");
    }
  });

  it("matches TLS / certificate failures and prefers them over network", () => {
    for (const msg of [
      "x509: certificate signed by unknown authority",
      "tls: handshake failure",
      "x509: certificate has expired or is not yet valid: dial tcp",
    ]) {
      const r = explainRunError("", msg);
      expect(r, msg).not.toBeNull();
      expect(r!.headlineKey, msg).toBe("explain.tlsError");
    }
  });

  it("leaves unmapped codes to the raw-detail fallback", () => {
    expect(explainRunError("io", "read: connection reset")).toBeNull();
    expect(explainRunError("node_failed", "node x failed")).toBeNull();
  });

  it("prefers a specific message match over the generic code map", () => {
    // A bad_param code whose message is actually a missing-secret — the
    // message-based match is more specific and must win.
    const r = explainRunError("bad_param", 'secret "API_KEY" not found');
    expect(r!.headlineKey).toBe("explain.secretMissing");
    expect(r!.headlineValues).toEqual({ name: "API_KEY" });
  });
});
