// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Builds the JSON body the editor's "Send test event" button POSTs to
// /test-trigger, so a webhook flow can be exercised from the canvas without an
// external caller. Pure: no editor state, no network.

// buildTestEventSample produces the JSON object the "Send test event"
// button POSTs to /test-trigger. When the webhook trigger has
// public_form opted in, formFields names the exact form inputs the
// hosted form will collect — we mirror that shape so the test fires
// with the same payload the real form would. Per-field defaults pick
// realistic-looking values for common names (email, phone, message)
// so a non-techie watching the canvas light up sees believable data
// instead of "string" everywhere. Unknown fields fall back to "Sample
// <field>". A nil or empty formFields list reproduces the legacy
// {name, email, message, submitted_at} sample — used when a webhook
// trigger isn't form-backed.
export function buildTestEventSample(formFields?: string[]): Record<string, string> {
  if (!formFields || formFields.length === 0) {
    return {
      message: "Test event from Dazyflow",
      name: "Jane Example",
      email: "jane@example.com",
      submitted_at: new Date().toISOString(),
    };
  }
  const sample: Record<string, string> = {};
  for (const raw of formFields) {
    const field = raw.trim();
    if (!field) continue;
    sample[field] = sampleValueFor(field);
  }
  return sample;
}

// sampleValueFor picks a plausible-looking value for a form field
// based on its name. Matching is on the lowercased name so "Email"
// and "email" both resolve to the same default. The catch-all
// produces a label like "Sample phone" rather than an empty string
// so the value is visibly distinguishable in a downstream Slack post
// or store row during testing.
function sampleValueFor(field: string): string {
  const f = field.toLowerCase();
  if (f === "email" || f.endsWith("_email")) return "jane@example.com";
  if (f === "name" || f.endsWith("_name")) return "Jane Example";
  if (f === "message" || f === "body" || f === "notes" || f === "comment" || f === "comments") {
    return "Test event from Dazyflow";
  }
  if (f === "phone" || f === "telephone" || f === "mobile") return "+1 555 0123";
  if (f === "company" || f === "organisation" || f === "organization") return "Acme AB";
  if (f === "subject") return "Test event";
  if (f === "submitted_at" || f === "created_at" || f === "timestamp") {
    return new Date().toISOString();
  }
  return `Sample ${field}`;
}
