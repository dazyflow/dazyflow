// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package value

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nyaruka/phonenumbers"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
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
			Description: "Hold a phone number — type it inline or wire a string into the 'phone' input — and emit it as clean E.164 (+46701234567) on 'out', but only after checking it's a real, dialable number. Local formats are understood: with Default region SE, \"070-123 45 67\" becomes \"+46701234567\". A number that isn't valid fails the node up front instead of surfacing as a cryptic error when a later SMS step rejects it. It also decomposes the number so you can act on its parts without string surgery: 'country' (SE), 'national' (701234567), and 'type' (mobile / fixed_line / …). Feed 'out' straight into the 46elks or Twilio SMS drops.",
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
				{Port: "out", Label: "E.164", MIME: []string{"text/plain"}},
				{Port: "country", Label: "Country", MIME: []string{"text/plain"}},
				{Port: "flag", Label: "Flag", MIME: []string{"text/plain"}},
				{Port: "national", Label: "National number", MIME: []string{"text/plain"}},
				{Port: "type", Label: "Type", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"phone":{"type":"string","title":"Phone","description":"A phone number — local (070-123 45 67) or international (+46701234567). Type it here, or wire a string into the 'phone' input."},
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

// regionToFlag turns an ISO 3166 alpha-2 region code ("SE") into its flag
// emoji (🇸🇪) by mapping each letter to its Unicode regional indicator symbol
// (A→U+1F1E6). Returns "" for anything that isn't exactly two ASCII letters —
// including the "001" pseudo-region libphonenumber uses for non-geographic
// numbers (satellite, +800 toll-free), which has no flag. Note: whether the
// emoji renders as a flag or as two letters depends on the viewer's font —
// modern browsers (the web UI) show flags; some terminals and Windows don't.
func regionToFlag(code string) string {
	if len(code) != 2 {
		return ""
	}
	var flag []rune
	for i := 0; i < 2; i++ {
		c := code[i]
		if c < 'A' || c > 'Z' {
			return ""
		}
		flag = append(flag, 0x1F1E6+rune(c-'A'))
	}
	return string(flag)
}

func executePhone(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	// Wired 'phone' input wins over the inline param (params.TextInputOr), so the
	// number can be computed upstream or set on the node.
	raw, ok := params.TextInputOr(job, "phone", params.StringDefault(job.Params, "phone", ""))
	if !ok {
		return params.Err(job, "bad_input", "the wired 'phone' input must be text"), nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return params.Err(job, "bad_param", "phone is required: wire the 'phone' input or set the phone param"), nil
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
		return params.Err(job, "bad_param", "not a valid phone number for region "+region+": "+raw), nil
	}

	e164 := phonenumbers.Format(num, phonenumbers.E164)
	national := strconv.FormatUint(num.GetNationalNumber(), 10)
	regionCode := phonenumbers.GetRegionCodeForNumber(num)
	typeLabel := numberTypeLabel(phonenumbers.GetNumberType(num))
	flag := regionToFlag(regionCode)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":      {MIME: "text/plain", Inline: e164},
			"country":  {MIME: "text/plain", Inline: regionCode},
			"flag":     {MIME: "text/plain", Inline: flag},
			"national": {MIME: "text/plain", Inline: national},
			"type":     {MIME: "text/plain", Inline: typeLabel},
			// Fuller decomposition for templating: the numeric calling code (46),
			// and the pretty national/international renderings.
			"meta": {MIME: "application/json", Inline: map[string]any{
				"e164":          e164,
				"country":       regionCode,
				"flag":          flag,
				"calling_code":  int(num.GetCountryCode()),
				"national":      national,
				"type":          typeLabel,
				"national_fmt":  phonenumbers.Format(num, phonenumbers.NATIONAL),
				"international": phonenumbers.Format(num, phonenumbers.INTERNATIONAL),
			}},
		},
	}, nil
}
