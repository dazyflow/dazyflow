// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// resendOutcome maps the verification-resend endpoint's three possible answers
// onto what the banner should say next.
//
// The distinction worth naming: a 200 is not the same as "an email is on its
// way". POST /me/verification/resend answers
//
//   {sent: true}                            — really sent
//   {sent: false, already_verified: true}   — nothing to send, they're done
//   502                                     — the mailer is down or unset
//
// Collapsing those into "it didn't throw, so say it was sent" is what made the
// resend button fail silently on a deployment with no working mailer: the
// button un-greyed, the banner went on claiming a link had been sent, and the
// only evidence of the 502 was in the network tab. A new owner's one route out
// of the invite gate looked like it worked every time they pressed it.
export type ResendOutcome = "verified" | "sent" | "failed";

export function resendOutcome(
  res: { sent?: boolean; already_verified?: boolean } | null,
): ResendOutcome {
  // null = the call threw. The route's only failure mode is "couldn't send".
  if (!res) return "failed";
  if (res.already_verified) return "verified";
  return res.sent ? "sent" : "failed";
}
