import { describe, it, expect } from "vitest";
import { explainApiError } from "./explainApiError";
import { APIError } from "../api";

// The test t() echoes the key so we can assert which message was chosen
// without coupling to the actual translations.
const t = (k: string) => k;

describe("explainApiError", () => {
  it("maps transport failure (status 0) to a network message", () => {
    expect(explainApiError(new APIError(0, "network error"), t)).toBe(
      "apiError.network",
    );
  });

  it("maps 5xx to a server message, hiding the raw text", () => {
    expect(
      explainApiError(new APIError(503, "service unavailable"), t),
    ).toBe("apiError.server");
  });

  it("treats a sign-in 401/403 as invalid credentials", () => {
    expect(
      explainApiError(new APIError(401, "auth: invalid credential"), t, "signin"),
    ).toBe("apiError.signinInvalid");
    expect(
      explainApiError(new APIError(403, "account locked"), t, "signin"),
    ).toBe("apiError.signinInvalid");
  });

  it("maps a bare 401 (no context) to session expired", () => {
    expect(explainApiError(new APIError(401, "unauthorized"), t)).toBe(
      "apiError.sessionExpired",
    );
  });

  it("maps a TOTP failure to a wrong-code message", () => {
    expect(
      explainApiError(new APIError(401, "totp: mismatch"), t, "totp"),
    ).toBe("apiError.totpInvalid");
  });

  it("detects an existing account on signup", () => {
    expect(
      explainApiError(new APIError(409, "email already registered"), t, "signup"),
    ).toBe("apiError.signupExists");
  });

  it("keeps an actionable signup validation message verbatim", () => {
    expect(
      explainApiError(
        new APIError(400, "password must be at least 12 characters"),
        t,
        "signup",
      ),
    ).toBe("password must be at least 12 characters");
  });

  it("hides a leaked Go/OS error behind the generic message", () => {
    for (const raw of [
      "dial tcp 1.2.3.4:443: connection refused",
      "open /workspace/x: permission denied",
      "strconv.ParseInt: parsing \"x\": invalid syntax",
      "decode body: invalid character '}' looking for value",
      "get oauth token: no tenant in context",
    ]) {
      expect(explainApiError(new APIError(400, raw), t), raw).toBe(
        "apiError.generic",
      );
    }
  });

  it("surfaces a server-authored human 4xx validation hint verbatim", () => {
    expect(
      explainApiError(new APIError(400, "value must not be empty"), t),
    ).toBe("value must not be empty");
  });

  it("prefers a structured code over the status fallback", () => {
    expect(
      explainApiError(new APIError(400, "whatever", "permission_denied"), t),
    ).toBe("apiError.forbidden");
  });

  it("maps status fallbacks: 403/404/409/429", () => {
    expect(explainApiError(new APIError(403, ""), t)).toBe("apiError.forbidden");
    expect(explainApiError(new APIError(404, ""), t)).toBe("apiError.notFound");
    expect(explainApiError(new APIError(409, ""), t)).toBe("apiError.conflict");
    expect(explainApiError(new APIError(429, ""), t)).toBe("apiError.rateLimited");
  });

  it("never trusts a non-APIError's text", () => {
    expect(explainApiError(new Error("kernel panic in component X"), t)).toBe(
      "apiError.generic",
    );
  });
});
