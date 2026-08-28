// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"encoding/json"
	"sort"
	"testing"
)

// A dropdown with no `enumNames` shows the RAW value: the Regex step's mode
// offered "extract / replace / split / match", lowercase, in every language,
// because nothing said what to call them. The values are API vocabulary and
// have to stay as they are; the labels are what a person reads, and they are a
// separate field nobody is forced to fill in — which is why fourteen of them
// were missing at once.
//
// So this asks for them. Add an enum without labels and it fails here, while
// the drop is being written, rather than showing an internal word to a user.
type enumProps struct {
	Properties map[string]struct {
		Enum      []json.RawMessage `json:"enum"`
		EnumNames []string          `json:"enumNames"`
	} `json:"properties"`
}

// rawValueEnums are the enums whose values ARE the right label: names a user
// knows from outside Dazyflow and may have to match exactly. Inventing display
// names for these would be worse than the raw value, not better — "Fetch"
// instead of GET helps nobody.
var rawValueEnums = map[string]bool{
	"http_request.method":  true,
	"http_download.method": true,
	"http_upload.method":   true,
	"webhook_send.method":  true,
}

func TestEnumsHaveDisplayNames(t *testing.T) {
	var missing []string
	for _, d := range allDrops(t) {
		if len(d.manifest.ParamsSchema) == 0 {
			continue
		}
		var s enumProps
		if err := json.Unmarshal(d.manifest.ParamsSchema, &s); err != nil {
			continue // TestParamNaming_Conventions reports the parse failure
		}
		for name, prop := range s.Properties {
			if len(prop.Enum) == 0 || rawValueEnums[d.id+"."+name] {
				continue
			}
			if len(prop.EnumNames) == 0 {
				missing = append(missing, d.id+"."+name)
				continue
			}
			if len(prop.EnumNames) != len(prop.Enum) {
				t.Errorf("%s.%s: %d enum values but %d enumNames — the dropdown would label the wrong option",
					d.id, name, len(prop.Enum), len(prop.EnumNames))
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("enum without enumNames: %s — give each value a display name "+
			"(and its Swedish in web/src/i18n/drops/fields.sv.ts), or add it to "+
			"rawValueEnums if the value is already the name a user knows", m)
	}
}
