// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// orgFromHost derives the org slug encoded in a per-org subdomain host.
// When the deployment sets a wildcard domain (e.g. "dazyflow.app"), a
// visit to "acme.dazyflow.app" should preselect org=acme on the sign-in
// page. This maps the browser hostname to that slug, or "" when the host
// isn't a usable org subdomain.
//
// Returns "" (no org) when:
//   - no wildcard domain is configured,
//   - the host is the apex itself ("dazyflow.app"),
//   - the label is multi-level ("a.b.dazyflow.app") — only single-label
//     subdomains map to an org,
//   - the label isn't a valid DNS-ish slug, or
//   - the label is a reserved name we never hand out to an org.
//
// Reserved labels are the infrastructure/marketing hosts a wildcard
// record would otherwise capture. They simply don't resolve to an org,
// so the sign-in page falls back to its no-org behaviour for them.
const RESERVED = new Set([
  "www",
  "app",
  "api",
  "admin",
  "auth",
  "static",
  "assets",
  "cdn",
  "mail",
  "smtp",
  "ftp",
  "ns",
  "ns1",
  "ns2",
  "blog",
  "docs",
  "status",
  "help",
  "support",
  "registry",
]);

// A conservative DNS label: 1-63 chars, lowercase alphanumerics and
// internal hyphens, no leading/trailing hyphen.
const LABEL = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/;

export function orgFromHost(hostname: string, wildcardDomain: string): string {
  if (!wildcardDomain) return "";
  const host = hostname.toLowerCase().split(":")[0];
  const domain = wildcardDomain.toLowerCase().replace(/^\.+/, "");
  const suffix = "." + domain;
  if (!host.endsWith(suffix)) return "";
  const label = host.slice(0, host.length - suffix.length);
  if (!label || label.includes(".")) return "";
  if (!LABEL.test(label)) return "";
  if (RESERVED.has(label)) return "";
  return label;
}
