// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"encoding/json"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"

	_ "github.com/dazyflow/dazyflow/drops" // register the real catalog
)

func TestEventTriggerModulesMatchCatalog(t *testing.T) {
	t.Parallel()
	notEvents := map[string]bool{
		"cron_trigger":  true, // scheduler (cron)
		"webhook_input": true, // inbound HTTP, checked for a secret/form
		"request_input": true, // inbound HTTP that waits for a Reply, checked for a secret
		"form_input":    true, // the hosted form; its presence is the opt-in
	}
	var missing []string
	for id, m := range engine.Default.Manifests() {
		// The interval-driven ones are the scheduler's, and their own list is
		// checked by TestPollTriggerModulesMatchCatalog below.
		if m.Category != "trigger" || notEvents[id] ||
			core.PollTriggerModules[id] || core.EventTriggerModules[id] {
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

// The poll half of the same guard. A trigger the scheduler is supposed to fire
// on an interval needs two things to be true at once — core.PollTriggerModules
// has to know its module id, and its params_schema has to declare the
// interval_seconds the scheduler reads — and neither half fails loudly on its
// own: the drop appears on the canvas, accepts an interval, and simply never
// fires.
func TestPollTriggerModulesMatchCatalog(t *testing.T) {
	t.Parallel()
	for id := range core.PollTriggerModules {
		m, ok := engine.Default.Manifests()[id]
		if !ok {
			t.Errorf("core.PollTriggerModules lists %q, which is not in the catalog", id)
			continue
		}
		if m.Category != "trigger" {
			t.Errorf("%s is in core.PollTriggerModules but its category is %q, not \"trigger\"", id, m.Category)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(m.ParamsSchema, &schema); err != nil {
			t.Errorf("%s: params_schema does not parse: %v", id, err)
			continue
		}
		if _, ok := schema.Properties["interval_seconds"]; !ok {
			t.Errorf("%s is scheduled off its interval_seconds param, which its "+
				"params_schema does not declare — the scheduler would never pick it up", id)
		}
	}
}
