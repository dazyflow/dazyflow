// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import {
  type TokenLabels,
  tokenChipLabel,
  tokenizeValue,
} from "../components/editor/nodeCardShared";

// Preview-time rendering of ${…} references in a value that is shown to a
// user as CONTENT rather than as a field — today the email preview's subject
// line and body.
//
// The node card already holds the line that raw token syntax is never a thing
// to show a user (see TokenText): a reference's wire format is not its name.
// The email preview was sending the drop's raw params straight to the render
// endpoint, so the one surface whose whole job is "this is what your recipient
// gets" was the surface still showing "Re: ${upstream.gmail_1.out[0].subject}".
//
// The preview cannot resolve a reference to a real value — nothing has run —
// so it substitutes the same words the {} menu and the node-card chips use.
// That says both what will be there and that it isn't literal text.
export function fillTokensForPreview(
  value: string,
  labels?: TokenLabels,
  // wrap decorates each substituted label. The default is plain text, for a
  // subject line (html/template escapes .Subject, so markup would arrive as
  // visible tags); the body passes a marker span — and must escape, since the
  // label carries user-set step names into raw HTML.
  wrap: (label: string) => string = (s) => s,
): string {
  return tokenizeValue(value)
    .map((seg) =>
      seg.kind === "text" ? seg.text : wrap(tokenChipLabel(seg.token, labels)),
    )
    .join("");
}

function escapeHTML(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// previewTokenSpan is the body's decoration: inline styles, not a class, since
// the preview is rendered in a sandboxed iframe that has none of the app's
// CSS. Dashed underline + a tint reads as "a value lands here" in every mail
// client's rendering of the same markup.
export function previewTokenSpan(label: string): string {
  return (
    '<span style="border-bottom:1px dashed currentColor;opacity:.7">' +
    escapeHTML(label) +
    "</span>"
  );
}
