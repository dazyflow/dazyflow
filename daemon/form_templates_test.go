// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// The hosted form is served only for form_input nodes (publicFormConfig), so a
// template that means to offer a form but declares the trigger some other way
// ships a flow whose form link 404s. Composition tests can't see this: such a
// graph validates fine against the catalog.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

var describesAForm = regexp.MustCompile(`\bforms?\b`)

func TestShippedFormTemplatesServeAForm(t *testing.T) {
	const dir = "../web/public/templates"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("templates dir not found (%v)", err)
	}

	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "index.json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var g core.Graph
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}

		for _, n := range g.Nodes {
			// public_form was webhook_input's opt-in before form_input existed.
			// Nothing reads it now, so it silently means "no form".
			if _, dead := n.Params["public_form"]; dead {
				t.Errorf("%s: node %q sets the dead public_form param — use the form_input module", e.Name(), n.ID)
			}
		}

		// Whole word: "format" in an unrelated description is not a form.
		if !describesAForm.MatchString(strings.ToLower(g.Name + " " + g.Description)) {
			continue
		}
		seen++
		if _, _, ok := publicFormConfig(g); !ok {
			t.Errorf("%s: describes a form but publicFormConfig serves none — needs a form_input node", e.Name())
		}
	}
	if seen == 0 {
		t.Error("no form-describing template found — the guard is checking nothing")
	}
	t.Logf("checked %d form templates", seen)
}
