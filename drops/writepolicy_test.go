// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"testing"
)

// Manifest.DedupeWrites and Manifest.Idempotent are hand-set bools that decide
// whether the engine may replay a step or must replay its RECORDED RESULT. Get
// them wrong on a step that writes to the outside world and an expired-lease
// reclaim sends a second email, books a second slot, or issues a second
// refund — and every existing test still passes, because the drop's own tests
// exercise one clean run.
//
// That is the same shape of hazard passthrough_test.go was written for, and it
// gets the same two-part guard: the drops that opt in today are pinned by name
// (the regression case — someone refactoring a manifest and dropping the
// flag), and the declaration is checked for internal consistency (the
// new-drop case, where a contradictory pair means the author had one of the
// two backwards).
//
// engine/writededupe_test.go covers the MECHANISM this protects; nothing
// covered which drops ask for it.
//
// dedupingWrites is every drop that performs a non-idempotent external write
// and relies on engine-side dedupe to recover safely. Removing the flag from
// any of these reintroduces the hole, so they are pinned by name. A new write
// drop belongs in this list; a drop that legitimately becomes idempotent
// (because its protocol made the write repeatable) should be removed from it
// in the same commit that changes the manifest, with the reason in the
// manifest comment.
var dedupingWrites = []string{
	"caldav_create_event",
	"discord_send_message",
	"drive_upload",
	"elks_send_sms",
	"email_send",
	"fortnox_create_customer",
	"fortnox_create_invoice",
	"gcal_create_event",
	"github_add_comment",
	"github_create_issue",
	"gmail_send_email",
	"homeassistant_call_service",
	"klarna_capture_order",
	"klarna_refund_order",
	"mqtt_publish",
	"notion_create_page",
	"nshift_create_shipment",
	"nshift_delete_shipment",
	"ntfy",
	"sheets_append_row",
	"slack_send_message",
	"stripe_cancel_subscription",
	"stripe_create_customer",
	"stripe_create_payment_link",
	"stripe_create_refund",
	"stripe_send_invoice",
	"twilio_send_sms",
	"webhook_send",
}

func TestWritePolicy_DedupingDropsStillOptIn(t *testing.T) {
	byID := map[string]bool{}
	for _, d := range allDrops(t) {
		byID[d.id] = d.manifest.DedupeWrites
	}
	for _, id := range dedupingWrites {
		dedupe, present := byID[id]
		if !present {
			t.Errorf("%s is pinned as a deduping write but no longer exists — remove it from the list if the drop was renamed or dropped", id)
			continue
		}
		if !dedupe {
			t.Errorf("%s no longer sets DedupeWrites: a recovered run will write a second time (a second email, booking, or refund)", id)
		}
	}
}

// The inverse: a drop that opts into dedupe while calling itself idempotent
// has one of the two backwards. Dedupe exists precisely because the write
// CAN'T be repeated safely — if it can, the drop wants Idempotent and no
// dedupe, and the engine may simply retry it.
func TestWritePolicy_DedupeImpliesNotIdempotent(t *testing.T) {
	for _, d := range allDrops(t) {
		if d.manifest.DedupeWrites && d.manifest.Idempotent {
			t.Errorf("%s declares both DedupeWrites and Idempotent — dedupe is for writes that can't be repeated, so one of these is backwards", d.id)
		}
	}
}
