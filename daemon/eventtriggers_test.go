// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"

	_ "github.com/dazyflow/dazyflow/drops" // register the real catalog
)

func TestEventTriggerModulesMatchCatalog(t *testing.T) {
	t.Parallel()
	notEvents := map[string]bool{
		"cron_trigger":              true, // scheduler
		"poll_trigger":              true, // scheduler
		"google_form_trigger":       true, // scheduler (interval)
		"ticketmaster_on_new_event": true, // scheduler (interval)
		"webhook_input":             true, // inbound HTTP, checked for a secret/form
		"request_input":             true, // inbound HTTP that waits for a Reply, checked for a secret
		"form_input":                true, // the hosted form; its presence is the opt-in
	}
	var missing []string
	for id, m := range engine.Default.Manifests() {
		if m.Category != "trigger" || notEvents[id] || core.EventTriggerModules[id] {
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		t.Errorf("trigger drops missing from core.EventTriggerModules: %v\n"+
			"Add them there (or to notEvents here if they fire via the scheduler), "+
			"or a flow triggered only by one will report as manual-only.", missing)
	}
	for id := range core.EventTriggerModules {
		if _, ok := engine.Default.Manifests()[id]; !ok {
			t.Errorf("core.EventTriggerModules lists %q, which is not in the catalog", id)
		}
	}
}
