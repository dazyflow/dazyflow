// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
    expect(explainApiError(new APIError(503, "service unavailable"), t)).toBe(
      "apiError.server",
    );
  });

  it("treats a sign-in 401/403 as invalid credentials", () => {
    expect(
      explainApiError(
        new APIError(401, "auth: invalid credential"),
        t,
        "signin",
      ),
    ).toBe("apiError.signinInvalid");
    expect(
      explainApiError(new APIError(403, "account locked"), t, "signin"),
    ).toBe("apiError.signinInvalid");
  });

  // Regression: a self-hoster who changes DAZYFLOW_PORT without matching
  // DAZYFLOW_WEB_ORIGIN gets a 403 csrf_origin on every sign-in attempt. The
  // sign-in branch used to claim the credentials were wrong, which is the one
  // thing they aren't — and it sends the reader hunting through passwords for
  // a problem that lives in .env. Deployment-config codes win over the
  // surface's own reading of the status.
  it("reports a sign-in csrf_origin as a server setting, not a bad password", () => {
    expect(
      explainApiError(
        new APIError(
          403,
          'cookie-authenticated request from disallowed origin "http://localhost:8099" (CSRF defense)',
          "csrf_origin",
        ),
        t,
        "signin",
      ),
    ).toBe("apiError.csrfOriginSignin");
  });

  it("still maps csrf_origin outside sign-in to the generic origin message", () => {
    expect(
      explainApiError(
        new APIError(403, "disallowed origin (CSRF defense)", "csrf_origin"),
        t,
      ),
    ).toBe("apiError.csrfOrigin");
  });

  it("does not let a config code mask a genuine TOTP rejection", () => {
    expect(
      explainApiError(new APIError(401, "bad code", ""), t, "totp"),
    ).toBe("apiError.totpInvalid");
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
      explainApiError(
        new APIError(409, "email already registered"),
        t,
        "signup",
      ),
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
      'strconv.ParseInt: parsing "x": invalid syntax',
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
    expect(explainApiError(new APIError(403, ""), t)).toBe(
      "apiError.forbidden",
    );
    expect(explainApiError(new APIError(404, ""), t)).toBe("apiError.notFound");
    expect(explainApiError(new APIError(409, ""), t)).toBe("apiError.conflict");
    expect(explainApiError(new APIError(429, ""), t)).toBe(
      "apiError.rateLimited",
    );
  });

  it("keeps a clean 413 upload message verbatim", () => {
    expect(
      explainApiError(
        new APIError(413, "the file is too large — the upload limit is 200 MB"),
        t,
      ),
    ).toBe("the file is too large — the upload limit is 200 MB");
  });

  it("falls back to a friendly message for a raw 413 guard string", () => {
    expect(
      explainApiError(
        new APIError(413, "request body exceeds 10485760 bytes"),
        t,
      ),
    ).toBe("apiError.tooLarge");
    expect(
      explainApiError(new APIError(413, "http: request body too large"), t),
    ).toBe("apiError.tooLarge");
  });

  it("maps a bare 413 with no message to the friendly too-large message", () => {
    expect(explainApiError(new APIError(413, ""), t)).toBe("apiError.tooLarge");
  });

  it("maps the storage_full code (507) to actionable guidance, not a 5xx outage", () => {
    expect(
      explainApiError(
        new APIError(
          507,
          "upload of 12345 bytes would exceed the tenant storage limit",
          "storage_full",
        ),
        t,
      ),
    ).toBe("apiError.storageFull");
  });

  it("never trusts a non-APIError's text", () => {
    expect(explainApiError(new Error("kernel panic in component X"), t)).toBe(
      "apiError.generic",
    );
  });
});

// A 409 means different things per surface. On an approval it is benign — the
// decision was already made — and the generic "it conflicts with something
// that already exists or is in use" reads like a fault the user must fix.
describe("approval conflicts", () => {
  it("reads as already-decided on an approval surface", () => {
    expect(
      explainApiError(
        new APIError(409, "node is succeeded, not awaiting"),
        t,
        "approval",
      ),
    ).toBe("apiError.approvalDecided");
  });

  it("keeps the generic conflict message everywhere else", () => {
    expect(explainApiError(new APIError(409, "name taken"), t)).toBe(
      "apiError.conflict",
    );
  });

  // A 403 comes in two flavours. One names the permission the caller lacks —
  // written for whoever wired the API call. The other names the thing the
  // reader can go and do. Only the second beats the generic headline.
  describe("403 refusals", () => {
    it("shows a refusal that tells the user how to unblock themselves", () => {
      // The invite gate. Swallowing this told the ORGANIZATION OWNER to "ask
      // an admin", and hid the only way forward.
      const msg =
        "verify your email before inviting others — check your inbox or resend from the banner";
      expect(explainApiError(new APIError(403, msg, "forbidden"), t)).toBe(msg);
      // Same sentence on a legacy route that sets no structured code.
      expect(explainApiError(new APIError(403, msg), t)).toBe(msg);
    });

    it("shows other remedy-shaped refusals", () => {
      for (const msg of [
        "only the org owner can change another admin's roles",
        "only password-auth users can accept invitations",
        "account suspended",
        "you don't have access to file a ticket",
      ]) {
        expect(explainApiError(new APIError(403, msg, "forbidden"), t)).toBe(
          msg,
        );
      }
    });

    it("keeps the generic headline for scope demands", () => {
      for (const msg of [
        "organization:admin required",
        "graph:edit required",
        "organization:admin on this tenant (or platform:admin) required",
        "support agent role required",
        "platform:admin required to erase an account",
        "connecting a Google account requires organization:admin",
        "principal has no tenant",
        "admin_scope_required",
      ]) {
        expect(explainApiError(new APIError(403, msg, "forbidden"), t)).toBe(
          "apiError.forbidden",
        );
        expect(explainApiError(new APIError(403, msg), t)).toBe(
          "apiError.forbidden",
        );
      }
    });

    it("still hides technical strings and empty bodies", () => {
      expect(
        explainApiError(new APIError(403, "permission denied", "forbidden"), t),
      ).toBe("apiError.forbidden");
      expect(explainApiError(new APIError(403, "", "forbidden"), t)).toBe(
        "apiError.forbidden",
      );
      expect(
        explainApiError(new APIError(403, "no tenant in context"), t),
      ).toBe("apiError.forbidden");
    });

    it("does not let the sign-in surface leak a refusal reason", () => {
      // Telling a stranger at the login form WHY they were refused is how you
      // confirm an account exists. Context still wins.
      expect(
        explainApiError(
          new APIError(403, "account suspended", "forbidden"),
          t,
          "signin",
        ),
      ).toBe("apiError.signinInvalid");
    });
  });
});
