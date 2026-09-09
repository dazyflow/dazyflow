// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

// Every trigger is either fed by an inbound delivery or started by the
// scheduler, and the run path treats the two oppositely: an inbound trigger
// that was not seeded cannot have fired, while a scheduled one always can.
// A trigger in neither class — or in both — would fall through those rules with
// no defined behaviour, so the partition is held here rather than assumed.
func TestTriggerModulesArePartitioned(t *testing.T) {
	t.Parallel()
	modules := []string{
		WebhookInputModule, FormInputModule, RequestInputModule,
		"cron_trigger", "poll_trigger", "google_form_trigger",
		"ticketmaster_on_new_event",
	}
	for id := range EventTriggerModules {
		modules = append(modules, id)
	}
	for _, m := range modules {
		if !IsTriggerModule(m) {
			t.Errorf("%q is listed as a trigger here but IsTriggerModule says no", m)
			continue
		}
		inbound, scheduled := IsInboundEventTriggerModule(m), IsScheduledTriggerModule(m)
		if inbound == scheduled {
			t.Errorf("%q: inbound=%v scheduled=%v — a trigger must be exactly one",
				m, inbound, scheduled)
		}
	}
	// And nothing outside the trigger set may claim either class.
	for _, m := range []string{"delay", "ntfy", "http_request"} {
		if IsInboundEventTriggerModule(m) || IsScheduledTriggerModule(m) {
			t.Errorf("%q is not a trigger but claims a trigger class", m)
		}
	}
}
