// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/nyaruka/phonenumbers"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// phone.go is a typed source field, the phone-number sibling of url.go: the
// Text drop's inline-or-wire ergonomics, constrained to a phone number and
// normalized to E.164. Like url it declares an input port (so the number can be
// computed upstream) and VALIDATES at run time, failing the node on a bad
// number (bad_param) rather than emitting a `valid` boolean — a malformed
// number is a mistake to surface at the field, not a value to thread onward.
//
// Unlike url (which rides net/url from the stdlib), real phone parsing needs
// libphonenumber's metadata — region-aware parsing of local formats
// ("070-123 45 67" → +46701234567) and true validity (not just digit shape) —
// so this drop depends on github.com/nyaruka/phonenumbers. The `default_region`
// param (SE by default, matching the Nordic focus) is the region assumed when
// the number isn't already in +international form; it's ignored for a number
// that already starts with +.
func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "phone",
			Version:     "1.0",
			Label:       "Phone",
			Color:       "#10b981",
			Icon:        "phone",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"phone", "number", "sms", "e164", "msisdn", "validate", "normalize"},
			Description: "Hold a phone number — type it inline or connect a string into the 'phone' input — and emit it as clean E.164 (+46701234567) on 'out', but only after checking it's a real, dialable number. Local formats are understood: with Default region SE, \"070-123 45 67\" becomes \"+46701234567\". A number that isn't valid fails the step up front instead of surfacing as a cryptic error when a later SMS step rejects it. It also decomposes the number so you can act on its parts without string surgery: 'country' (SE), 'national' (701234567), and 'type' (mobile / fixed_line / …). Feed 'out' straight into the 46elks or Twilio SMS steps.",
			Summary:     "Validate and normalize a phone number to E.164, and emit its country, national number, and type.",
			Examples: []core.ParamsExample{
				{
					Title:  "A Swedish number in local format",
					Params: json.RawMessage(`{"phone":"070-123 45 67","default_region":"SE"}`),
					Notes:  "'out' is \"+46701234567\"; 'country' is \"SE\"; 'national' is \"701234567\"; 'type' is \"mobile\".",
				},
				{
					Title:  "Already international — region ignored",
					Params: json.RawMessage(`{"phone":"+44 20 7946 0958"}`),
					Notes:  "A number that already starts with + parses regardless of Default region.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Not marked Required: the number may instead be typed into the
				// `phone` param. The schema's required:["phone"] + the editor's
				// config check (a wired input satisfies it) enforce "type it OR
				// wire it" — mirrors url / rss / gmail_send_email.
				{Port: "phone", Label: "Phone", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "E.164", MIME: []string{"text/plain"}, Example: json.RawMessage(`"+46701234567"`)},
				{Port: "country", Label: "Country", MIME: []string{"text/plain"}, Example: json.RawMessage(`"SE"`)},
				{Port: "national", Label: "National number", MIME: []string{"text/plain"}, Example: json.RawMessage(`"070 123 45 67"`)},
				{Port: "type", Label: "Type", MIME: []string{"text/plain"}, Example: json.RawMessage(`"mobile"`)},
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"phone":{"type":"string","format":"tel","title":"Phone","description":"A phone number — local (070-123 45 67) or international (+46701234567). Type it here, or connect a string into the 'phone' input."},
					"default_region":{"type":"string","title":"Default region","default":"SE","description":"ISO 3166 alpha-2 country code (SE, NO, DK, FI, GB…) to assume when the number isn't written in +international form. Ignored for a number that already starts with +."}
				},
				"required":["phone"]
			}`),
			Idempotent: true,
		},
		Execute: executePhone,
	})
}

// numberTypeLabel maps libphonenumber's PhoneNumberType to a lowercase word for
// the 'type' pin — the values a downstream branch is likely to test.
func numberTypeLabel(t phonenumbers.PhoneNumberType) string {
	switch t {
	case phonenumbers.FIXED_LINE:
		return "fixed_line"
	case phonenumbers.MOBILE:
		return "mobile"
	case phonenumbers.FIXED_LINE_OR_MOBILE:
		return "fixed_line_or_mobile"
	case phonenumbers.TOLL_FREE:
		return "toll_free"
	case phonenumbers.PREMIUM_RATE:
		return "premium_rate"
	case phonenumbers.SHARED_COST:
		return "shared_cost"
	case phonenumbers.VOIP:
		return "voip"
	case phonenumbers.PERSONAL_NUMBER:
		return "personal_number"
	case phonenumbers.PAGER:
		return "pager"
	case phonenumbers.UAN:
		return "uan"
	case phonenumbers.VOICEMAIL:
		return "voicemail"
	default:
		return "unknown"
	}
}

func executePhone(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Wired 'phone' input wins over the inline param (params.TextInputOr), so the
	// number can be computed upstream or set on the node.
	raw, ok := params.TextInputOr(job, "phone", params.StringDefault(job.Params, "phone", ""))
	if !ok {
		return params.Err(job, "bad_input", "the connected 'phone' input must be text"), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return params.Err(job, "bad_param", "phone is required: connect the 'phone' input or set the phone param"), nil
	}

	// Uppercase the region: libphonenumber expects "SE", not "se". An empty
	// region only parses a +international number; a local number then errors,
	// which the messages below explain.
	region := strings.ToUpper(strings.TrimSpace(params.StringDefault(job.Params, "default_region", "SE")))

	num, err := phonenumbers.Parse(raw, region)
	if err != nil {
		return params.Err(job, "bad_param", "not a valid phone number: "+err.Error()+" (for a local number, set Default region to its country, e.g. SE)"), nil
	}
	if !phonenumbers.IsValidNumber(num) {
		// The number parsed but isn't a real, dialable number. Name the country
		// it actually resolved to — its own calling code when written in
		// international form (+…/00…), else the default region — so the message
		// isn't misleadingly about SE for a +1 / 0045 number the user wrote
		// internationally.
		where := "region " + region
		if resolved := phonenumbers.GetRegionCodeForNumber(num); resolved != "" && resolved != "ZZ" {
			where = "region " + resolved
		} else if cc := num.GetCountryCode(); cc != 0 {
			where = fmt.Sprintf("country code +%d", cc)
		}
		return params.Err(job, "bad_param", fmt.Sprintf("not a valid phone number for %s: %s", where, raw)), nil
	}

	e164 := phonenumbers.Format(num, phonenumbers.E164)
	national := strconv.FormatUint(num.GetNationalNumber(), 10)
	regionCode := phonenumbers.GetRegionCodeForNumber(num)
	typeLabel := numberTypeLabel(phonenumbers.GetNumberType(num))

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":      {MIME: "text/plain", Inline: e164},
			"country":  {MIME: "text/plain", Inline: regionCode},
			"national": {MIME: "text/plain", Inline: national},
			"type":     {MIME: "text/plain", Inline: typeLabel},
			// Fuller decomposition for templating: the numeric calling code (46),
			// and the pretty national/international renderings.
			"meta": {MIME: "application/json", Inline: map[string]any{
				"e164":          e164,
				"country":       regionCode,
				"calling_code":  int(num.GetCountryCode()),
				"national":      national,
				"type":          typeLabel,
				"national_fmt":  phonenumbers.Format(num, phonenumbers.NATIONAL),
				"international": phonenumbers.Format(num, phonenumbers.INTERNATIONAL),
			}},
		},
	}, nil
}
