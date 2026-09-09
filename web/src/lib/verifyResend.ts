// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// resendOutcome maps the verification-resend endpoint's three possible answers
// onto what the banner should say next.
//
// A 200 is not the same as "an email is on its way":
//
//   {sent: true}                            — really sent
//   {sent: false, already_verified: true}   — nothing to send, they're done
//   502                                     — the mailer is down or unset
//
// Collapsing those into "it didn't throw, so say it was sent" is what made the
// resend button fail silently on a deployment with no working mailer: the banner
// claimed a link had been sent and the only evidence of the 502 was in the
// network tab.
export type ResendOutcome = "verified" | "sent" | "failed";

export function resendOutcome(
  res: { sent?: boolean; already_verified?: boolean } | null,
): ResendOutcome {
  if (!res) return "failed";
  if (res.already_verified) return "verified";
  return res.sent ? "sent" : "failed";
}
