// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

// Writing a number down is the one formatting job a formula language cannot do
// well, because the answer depends on who is reading it: 1,234.50 in English
// and 1 234,50 in Swedish are the same number and different text, and a flow
// that mails a Swedish customer an English number looks broken to them.
//
// The separators are plain spaces rather than the non-breaking ones typography
// would ask for. A thousands separator that is an invisible non-ASCII
// character survives an email and then quietly breaks a spreadsheet lookup, a
// CSV column and every string comparison downstream — and nobody can see why.
type numberLocale struct {
	group    string
	decimal  string
	percent  string // what goes between the number and the % sign
	symbolAt string // "before" or "after" the amount
}

var numberLocales = map[string]numberLocale{
	"en": {group: ",", decimal: ".", percent: "", symbolAt: "before"},
	"sv": {group: " ", decimal: ",", percent: " ", symbolAt: "after"},
}

// currencySymbols covers what a Nordic business actually invoices in. Anything
// else falls back to its own code, which is what a bank statement would show.
var currencySymbols = map[string]string{
	"SEK": "kr", "NOK": "kr", "DKK": "kr", "ISK": "kr",
	"EUR": "€", "USD": "$", "GBP": "£", "JPY": "¥",
}

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "format_number",
			Version:  "1.0",
			Label:    "Change a number",
			Subtitle: "Round it, or write it out",
			Icon:     "hash",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{"number", "format", "round", "decimals", "currency", "money",
				"percent", "percentage", "thousands", "amount", "price", "total"},
			Description: "Round a number, or write it out the way a person reads it.\n\n" +
				"'Round it' is the one that changes the number itself — to a number of decimals, and up or down " +
				"rather than to the nearest when you need a price ceiling or a whole box count. Everything else " +
				"changes only how it is written.\n\n" +
				"'Write it out' gives it a fixed number of decimals and groups the thousands. 'As money' adds the " +
				"currency, and 'As a percentage' multiplies by a hundred and adds the sign — 0.25 becomes 25%.\n\n" +
				"How it is written depends on who reads it: 1,234.50 in English is 1 234,50 in Swedish, and money " +
				"goes $1,234.50 in one and 1 234,50 kr in the other. The step follows the flow's own language " +
				"unless you set 'Language' on it. Anything but the currencies with a symbol of their own is " +
				"written with its code, the way a bank statement writes it.\n\n" +
				"Both come out: the text on 'Result', and the number itself on 'Number' — so a rounded value can " +
				"go on to be compared or added up without being read back out of its own formatting.",
			Summary: "Round a number, or write it as text with decimals, thousands, currency or a percent sign, in the reader's language.",
			Examples: []core.ParamsExample{
				{
					Title:  "A price, written for the reader",
					Params: json.RawMessage(`{"op":"currency","currency":"SEK","decimals":2}`),
					Notes:  "1234.5 becomes \"1 234,50 kr\" for a Swedish flow and \"SEK 1,234.50\" for an English one.",
				},
				{
					Title:  "Round up to whole boxes",
					Params: json.RawMessage(`{"op":"round","round_mode":"up","decimals":0}`),
					Notes:  "17.2 becomes 18 — on 'Number' as a number, ready to multiply.",
				},
				{
					Title:  "A share as a percentage",
					Params: json.RawMessage(`{"op":"percent","decimals":1}`),
					Notes:  "0.2537 becomes \"25.4%\" — or \"25,4 %\" in Swedish.",
				},
				{
					Title:  "Plain, with two decimals and no grouping",
					Params: json.RawMessage(`{"op":"format","decimals":2,"group":false}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Number", Required: true},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result", MIME: []string{"text/plain"}},
				{Port: "value", Label: "Number", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"op":{"type":"string","title":"What to do","default":"format","enum":["format","round","currency","percent"],"enumNames":["Write it out","Round it","As money","As a percentage"],"description":"Rounding changes the number; the others change only how it is written."},
					"decimals":{"type":"integer","default":2,"minimum":0,"maximum":10,"title":"Decimals","description":"How many digits after the decimal point. 0 gives a whole number."},
					"round_mode":{"type":"string","default":"nearest","enum":["nearest","up","down"],"enumNames":["To the nearest","Always up","Always down"],"title":"Which way","x_visible_when":{"op":"round"},"description":"Nearest is the ordinary one. Up and down are for the cases where the answer has to cover something — boxes to order, a price ceiling."},
					"currency":{"type":"string","default":"SEK","title":"Currency","x_visible_when":{"op":"currency"},"description":"The three-letter code — SEK, EUR, USD, GBP. Ones with a symbol of their own get it; the rest are written with the code."},
					"group":{"type":"boolean","default":true,"title":"Group the thousands","description":"On writes 1 234 567 (or 1,234,567); off writes 1234567. Turn it off when the number is going somewhere that has to read it back as a number."},
					"locale":{"type":"string","default":"","enum":["","en","sv"],"enumNames":["Follow the flow's language","English","Svenska"],"title":"Language","description":"Which way round the separators go, and where a currency sits. Leave it on the flow's language (Settings → General) unless this one step should differ."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeFormatNumber,
	})
}

func executeFormatNumber(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required — wire in the number to change"), nil
	}
	n, ok := coerceNumber(ref.Inline)
	if !ok {
		return params.Err(job, "bad_input", fmt.Sprintf("expected a number, got %v", describeValue(ref.Inline))), nil
	}
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return params.Err(job, "bad_input", "that is not a number anything can be written as"), nil
	}

	op := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "op", "format")))
	decimals := params.ClampInt(params.IntDefault(job.Params, "decimals", 2), 0, 10)
	group := params.BoolDefault(job.Params, "group", true)
	loc := numberLocaleFor(job)

	switch op {
	case "format", "":
		return numberResult(job, n, writeNumber(n, decimals, group, loc)), nil

	case "round":
		rounded := roundTo(n, decimals, strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "round_mode", "nearest"))))
		return numberResult(job, rounded, writeNumber(rounded, decimals, group, loc)), nil

	case "percent":
		scaled := n * 100
		return numberResult(job, scaled,
			writeNumber(scaled, decimals, group, loc)+loc.percent+"%"), nil

	case "currency":
		code := strings.ToUpper(strings.TrimSpace(params.StringDefault(job.Params, "currency", "SEK")))
		if code == "" {
			return params.Err(job, "bad_param", "'Currency' is required — the three-letter code, like SEK"), nil
		}
		return numberResult(job, n, writeMoney(n, code, decimals, group, loc)), nil
	}
	return params.Err(job, "bad_param", fmt.Sprintf(
		"unknown 'What to do' value %q (expected format, round, currency or percent)", op)), nil
}

func numberResult(job core.Job, value float64, text string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":   {MIME: "text/plain", Inline: text},
			"value": {MIME: "application/json", Inline: value},
		},
	}
}

func numberLocaleFor(job core.Job) numberLocale {
	name := strings.TrimSpace(params.StringDefault(job.Params, "locale", ""))
	if name == "" {
		name = job.Language
	}
	// A language nothing here knows about reads numbers the English way,
	// which is what every machine format uses too.
	if loc, ok := numberLocales[strings.ToLower(strings.SplitN(name, "-", 2)[0])]; ok {
		return loc
	}
	return numberLocales["en"]
}

// writeNumber renders a number with a fixed number of decimals, grouped or
// not, in the reader's separators.
func writeNumber(n float64, decimals int, group bool, loc numberLocale) string {
	text := strconv.FormatFloat(n, 'f', decimals, 64)
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	whole, frac, _ := strings.Cut(text, ".")
	if group {
		whole = groupThousands(whole, loc.group)
	}
	if frac == "" {
		return sign + whole
	}
	return sign + whole + loc.decimal + frac
}

func groupThousands(digits, sep string) string {
	if len(digits) <= 3 || sep == "" {
		return digits
	}
	var b strings.Builder
	lead := len(digits) % 3
	if lead > 0 {
		b.WriteString(digits[:lead])
	}
	for i := lead; i < len(digits); i += 3 {
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

// writeMoney puts the amount and its currency together the way the reader's
// language does: the symbol in front in English, after in Swedish, and an
// unknown currency written as its code with a space either way.
func writeMoney(n float64, code string, decimals int, group bool, loc numberLocale) string {
	amount := writeNumber(n, decimals, group, loc)
	symbol, known := currencySymbols[code]
	if !known {
		symbol = code
	}
	if loc.symbolAt == "after" {
		return amount + " " + symbol
	}
	if known {
		// A symbol sits against the digits; a code needs the space that keeps
		// it from reading as part of the number.
		if strings.HasPrefix(amount, "-") {
			return "-" + symbol + amount[1:]
		}
		return symbol + amount
	}
	return symbol + " " + amount
}

// roundTo rounds half away from zero, which is what a spreadsheet does and
// what a person expects of a price. Up and down mean away from and toward
// zero's side of the line — a ceiling and a floor.
func roundTo(n float64, decimals int, mode string) float64 {
	scale := math.Pow(10, float64(decimals))
	switch mode {
	case "up":
		return math.Ceil(n*scale) / scale
	case "down":
		return math.Floor(n*scale) / scale
	default:
		return math.Round(n*scale) / scale
	}
}

// coerceNumber reads a number out of whatever an earlier step emitted,
// including text a person typed: "1 234,50" and "1,234.50" are both numbers
// somebody wrote down, and refusing them would send the author to the
// Expression step to do arithmetic on a string.
func coerceNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		return parseLooseNumber(t)
	}
	return 0, false
}

func parseLooseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Spaces group thousands in half of Europe; so does a non-breaking one,
	// which is what arrives when a number was copied out of a web page.
	s = strings.NewReplacer(" ", "", " ", "", " ", "", "'", "").Replace(s)

	lastComma := strings.LastIndexByte(s, ',')
	lastDot := strings.LastIndexByte(s, '.')
	switch {
	case lastComma >= 0 && lastDot >= 0:
		// Both present: whichever comes last is the decimal point, and the
		// other was grouping.
		if lastComma > lastDot {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.Replace(s, ",", ".", 1)
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	case lastComma >= 0:
		// A lone comma is a decimal point unless it is grouping three digits
		// off the end — "1,234" is a thousand two hundred, "1,23" is one and
		// a bit, and English writes both.
		if len(s)-lastComma == 4 {
			s = strings.ReplaceAll(s, ",", "")
		} else {
			s = strings.Replace(s, ",", ".", 1)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func describeValue(v any) string {
	if v == nil {
		return "nothing"
	}
	if s, ok := v.(string); ok {
		return fmt.Sprintf("the text %q", params.Truncate(s, 40))
	}
	return fmt.Sprintf("%T", v)
}
