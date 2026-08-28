// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { describe, expect, it } from "vitest";
import { APIError, isErrorCode, isHTTPStatus } from "./api";

describe("isErrorCode", () => {
  it("matches an APIError with the given structured code", () => {
    const err = new APIError(404, "no such flow", "not_found");
    expect(isErrorCode(err, "not_found")).toBe(true);
    expect(isErrorCode(err, "conflict")).toBe(false);
  });

  it("is false for a legacy error with no code", () => {
    const err = new APIError(409, "active run in progress");
    expect(isErrorCode(err, "conflict")).toBe(false);
  });

  it("is false for non-APIError values", () => {
    expect(isErrorCode(new Error("not_found"), "not_found")).toBe(false);
    expect(isErrorCode("not_found", "not_found")).toBe(false);
    expect(isErrorCode(null, "not_found")).toBe(false);
  });
});

describe("isHTTPStatus", () => {
  it("matches an APIError by HTTP status", () => {
    const err = new APIError(409, "conflict", "conflict");
    expect(isHTTPStatus(err, 409)).toBe(true);
    expect(isHTTPStatus(err, 404)).toBe(false);
  });

  it("is false for non-APIError values", () => {
    expect(isHTTPStatus(new Error("409"), 409)).toBe(false);
    expect(isHTTPStatus(undefined, 409)).toBe(false);
  });
});
