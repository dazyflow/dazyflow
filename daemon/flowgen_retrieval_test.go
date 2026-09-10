// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

// A retrieval eval for the step catalogue: plain-language asks against the step
// each one has to reach.
//
// This is the layer under tests/usecases/README.md. That corpus scores whole
// drafts and needs a vendor key; this one needs nothing, because the question is
// narrower — when the model searches the catalogue with the words a person
// actually used, does the right step come back near the top? A step the model
// cannot find is a step it will not wire, and the failure looks like a stupid
// model rather than a search that answered badly.
//
// Every ask is written in the USER's vocabulary, deliberately not the drop's: an
// ask that only passes because it quotes the summary it is meant to find proves
// nothing.

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/internal/svsearch"
)

// retrievalCase is one thing a person says and the steps that would answer it.
// `want` holds every acceptable answer, because plenty of asks have more than
// one right step: "summarise this" is served by any of the four providers'
// summarise steps, and preferring one of them is not the search's job.
type retrievalCase struct {
	ask  string
	want []string
}

var retrievalCases = []retrievalCase{
	// ---- Starting a flow -------------------------------------------------
	{"every morning at nine", []string{"cron_trigger"}},
	{"run this once a week", []string{"cron_trigger"}},
	{"check every five minutes", []string{"poll_trigger"}},
	{"a page on my website where people can write to me", []string{"form_input"}},
	{"public form anyone can fill in", []string{"form_input"}},
	{"when someone submits the contact form", []string{"form_input"}},
	{"when another system posts data to us", []string{"webhook_input"}},
	{"answer the caller that asked", []string{"request_input", "reply"}},
	{"when a new email arrives", []string{"gmail_search_messages", "imap_search_messages"}},
	{"when someone pays", []string{"stripe_on_payment"}},
	{"when a payment is declined", []string{"stripe_on_payment_failed"}},
	{"when somebody cancels their subscription", []string{"stripe_on_subscription_canceled"}},
	{"when a pull request is opened", []string{"github_on_new_pr"}},
	{"when someone mentions us in slack", []string{"slack_on_mention"}},
	{"when a new blog post appears in the feed", []string{"rss"}},
	{"tell me when a web page changes", []string{"web_watch"}},
	{"tell me when my website goes down", []string{"site_check"}},
	{"when a google form gets a new answer", []string{"google_form_trigger"}},

	// ---- Telling people something ---------------------------------------
	{"post a message in slack", []string{"slack_send_message"}},
	{"send it to our discord", []string{"discord_send_message"}},
	{"text me", []string{"twilio_send_sms", "elks_send_sms"}},
	{"send an sms", []string{"twilio_send_sms", "elks_send_sms"}},
	{"ping my phone", []string{"ntfy"}},
	{"push notification to my phone", []string{"ntfy"}},
	{"email me", []string{"email_send", "gmail_send_email"}},
	{"send an email from my gmail", []string{"gmail_send_email"}},
	{"email through my own mail server", []string{"email_send"}},
	{"tell another service something happened", []string{"webhook_send"}},

	// ---- Spreadsheets and tables ----------------------------------------
	{"add a row to a google sheet", []string{"sheets_append_row"}},
	{"save it to a spreadsheet", []string{"sheets_append_row", "excel_write"}},
	{"read a range from a sheet", []string{"sheets_read_range"}},
	{"mark the row as done", []string{"sheets_update_cells"}},
	{"turn the sheet into a pdf", []string{"sheets_export_pdf"}},
	{"read an excel file", []string{"excel_read"}},
	{"keep the answers in dazyflow without any setup", []string{"builtin_store_append"}},
	{"look up what we stored earlier", []string{"builtin_store_find", "builtin_store_query"}},

	// ---- Databases -------------------------------------------------------
	{"insert the rows into postgres", []string{"postgres_insert_rows"}},
	{"query our mysql database", []string{"mysql_query"}},
	{"insert or update so nothing duplicates", []string{"postgres_upsert_rows", "mysql_upsert_rows", "sqlite_upsert_rows"}},

	// ---- Files -----------------------------------------------------------
	{"save the attachment in google drive", []string{"drive_upload"}},
	{"list the files in drive", []string{"drive_list_files"}},
	{"the files attached to the email", []string{"gmail_get_attachments", "imap_get_attachments"}},
	{"grab a file off an sftp server", []string{"sftp_download_file"}},
	{"put a payment file on sftp", []string{"sftp_upload_file"}},
	{"join all the invoices into one pdf", []string{"pdf_merge"}},
	{"split the scanned pdf into pages", []string{"pdf_split"}},
	{"download the file from a url", []string{"http_download"}},

	// ---- AI --------------------------------------------------------------
	{"summarise it", []string{"claude_summarize", "gpt_summarize", "gemini_summarize", "ollama_summarize"}},
	{"sort each email into a category", []string{"claude_classify", "gpt_classify", "gemini_classify", "ollama_classify"}},
	{"pull the invoice number out of the text", []string{"claude_extract", "gpt_extract", "gemini_extract", "ollama_extract"}},
	{"write a suggested reply", []string{"claude_draft_reply", "gpt_draft_reply", "gemini_draft_reply", "ollama_draft_reply"}},
	{"run the ai on my own machine", []string{"ollama", "ollama_summarize"}},

	// ---- Nordic / EU connectors -----------------------------------------
	{"raise an invoice in fortnox", []string{"fortnox_create_invoice"}},
	{"which fortnox invoices are unpaid", []string{"fortnox_list_invoices"}},
	{"refund the klarna order", []string{"klarna_refund_order"}},
	{"look up a swedish company by organisation number", []string{"roaring_company_overview", "roaring_company_search"}},
	{"book a parcel with the carrier", []string{"nshift_create_shipment"}},
	{"where is the parcel now", []string{"nshift_get_shipment"}},
	{"swedish weather forecast", []string{"smhi_forecast", "smhi_current"}},
	{"will it rain tomorrow", []string{"smhi_forecast", "openmeteo_forecast", "weather_forecast"}},

	// ---- Billing ---------------------------------------------------------
	{"refund the payment", []string{"stripe_create_refund"}},
	{"bill the customer and email the invoice", []string{"stripe_send_invoice"}},
	{"a link they can pay with", []string{"stripe_create_payment_link"}},
	{"find the customer by email", []string{"stripe_search_customers"}},
	{"cancel their subscription", []string{"stripe_cancel_subscription"}},

	// ---- Calendar --------------------------------------------------------
	{"put it on my google calendar", []string{"gcal_create_event"}},
	{"what is booked tomorrow", []string{"gcal_list_events", "caldav_list_events"}},
	{"move the booking to another time", []string{"caldav_update_event"}},

	// ---- Shaping the data ------------------------------------------------
	{"turn the rows into a message", []string{"render_text"}},
	{"show it as a table in the email", []string{"render_table"}},
	{"only the rows where the amount is over a thousand", []string{"compute_rows", "split_rows"}},
	{"remove the duplicates", []string{"dedupe_rows"}},
	{"count them up per country", []string{"group_aggregate"}},
	{"do it for every row one at a time", []string{"for_each"}},
	{"wait until someone approves it", []string{"await_approval"}},
	{"go one way or the other depending on the answer", []string{"branch", "if", "switch"}},
	{"put the two branches back together", []string{"merge"}},

	// ---- Other ------------------------------------------------------------
	{"open a github issue", []string{"github_create_issue"}},
	{"add a page in notion", []string{"notion_create_page"}},
	{"turn the lights on", []string{"homeassistant_call_service"}},
	{"run a script on my own server", []string{"run_on_runner"}},
	{"call an api", []string{"http_request"}},
	{"what are the coordinates of this address", []string{"geo_location"}},
}

// rankedIDs returns the ranked step ids the search gives back for an ask.
func rankedIDs(mans []core.Manifest, ask string) []string {
	hits := rankManifests(mans, ask)
	out := make([]string, 0, len(hits))
	for _, m := range hits {
		out = append(out, m.ID)
	}
	return out
}

func anyOf(want, got []string) bool {
	for _, g := range got {
		for _, w := range want {
			if g == w {
				return true
			}
		}
	}
	return false
}

// The floors are the measured score, so an improvement elsewhere that quietly
// costs retrieval shows up here. Raise them when the score rises; never lower
// one to make a change pass.
const (
	minHitAt1 = 72
	minHitAt5 = 95
	// Swedish is lower on purpose: the catalogue is English and the table
	// covers the vocabulary someone thought to add, not the language.
	minSwedishHitAt5 = 88
)

func TestRetrieval_CatalogueAnswersPlainLanguage(t *testing.T) {
	mans := allManifests()

	var at1, at5, total int
	var missed, weak []string
	for _, c := range retrievalCases {
		total++
		got := rankedIDs(mans, c.ask)
		top1, top5 := got, got
		if len(top1) > 1 {
			top1 = top1[:1]
		}
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		switch {
		case anyOf(c.want, top1):
			at1++
			at5++
		case anyOf(c.want, top5):
			at5++
			weak = append(weak, c.ask+"  (want "+strings.Join(c.want, "/")+", got "+strings.Join(top5, ", ")+")")
		default:
			shown := got
			if len(shown) > 5 {
				shown = shown[:5]
			}
			if len(shown) == 0 {
				shown = []string{"NOTHING"}
			}
			missed = append(missed, c.ask+"  (want "+strings.Join(c.want, "/")+", got "+strings.Join(shown, ", ")+")")
		}
	}

	pct1 := 100 * at1 / total
	pct5 := 100 * at5 / total
	t.Logf("retrieval over %d plain-language asks: hit@1 %d%% (%d/%d), hit@5 %d%% (%d/%d)",
		total, pct1, at1, total, pct5, at5, total)
	if len(weak) > 0 {
		t.Logf("in the top five but not first (%d):\n  %s", len(weak), strings.Join(weak, "\n  "))
	}
	if len(missed) > 0 {
		t.Logf("not in the top five (%d):\n  %s", len(missed), strings.Join(missed, "\n  "))
	}
	if pct1 < minHitAt1 {
		t.Errorf("hit@1 %d%% is below the floor of %d%%", pct1, minHitAt1)
	}
	if pct5 < minHitAt5 {
		t.Errorf("hit@5 %d%% is below the floor of %d%%", pct5, minHitAt5)
	}
}

// Every id named as an acceptable answer must exist, or a case silently stops
// testing anything the day a step is renamed.
func TestRetrieval_CasesNameRealSteps(t *testing.T) {
	known := map[string]bool{}
	for _, m := range allManifests() {
		known[m.ID] = true
	}
	var bad []string
	for _, c := range retrievalCases {
		for _, w := range c.want {
			if !known[w] {
				bad = append(bad, w+" (ask: "+c.ask+")")
			}
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("cases name %d step ids that do not exist:\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}

// Swedish asks against an English catalogue.
//
// The catalogue is authored in English on purpose — it is the contract the API,
// the MCP tools and the generator are grounded on, and only the human UI
// localises. The editor's palette copes by translating the QUERY through a
// Swedish alias table (web/src/lib/dropSearch.ts); this side has no equivalent,
// so Swedish retrieval is weak and the floors below say so.
//
// What must NOT happen is a confidently wrong answer. Before the id-substring
// floor, Swedish function words matched inside English ids — "min" hit ge-min-i,
// "en" hit builtin_store_app-en-d — so unrelated asks returned the same
// irrelevant block. An empty result the model can act on beats a plausible wrong
// one it cannot.
var swedishCases = []retrievalCase{
	// Brand names first: the same word in both languages, so these worked even
	// before the vocabulary was shared.
	{"skapa en faktura i fortnox", []string{"fortnox_create_invoice"}},
	{"återbetala klarna-ordern", []string{"klarna_refund_order"}},
	{"boka en frakt med nshift", []string{"nshift_create_shipment"}},
	{"skicka ett meddelande i slack", []string{"slack_send_message"}},
	{"lägg till en rad i google sheets", []string{"sheets_append_row"}},
	{"lägg in det i min google kalender", []string{"gcal_create_event"}},
	{"öppna ett ärende på github", []string{"github_create_issue"}},
	{"skicka sms via twilio", []string{"twilio_send_sms"}},
	{"väderprognos från smhi", []string{"smhi_forecast", "smhi_current"}},
	{"ladda upp filen till google drive", []string{"drive_upload"}},
	// And these, which name no brand and so depend entirely on the table.
	{"varje morgon klockan nio", []string{"cron_trigger"}},
	{"ett formulär där folk kan skriva till mig", []string{"form_input"}},
	{"skicka e-post till mig", []string{"email_send", "gmail_send_email"}},
	{"pinga min telefon", []string{"ntfy"}},
	{"lägg till en rad i kalkylarket", []string{"sheets_append_row", "excel_write"}},
	{"läs ett intervall från kalkylbladet", []string{"sheets_read_range", "excel_read"}},
	{"spara svaren i en samling", []string{"builtin_store_append"}},
	{"sammanfatta texten", []string{"claude_summarize", "gpt_summarize", "gemini_summarize", "ollama_summarize"}},
	{"klassificera varje e-post", []string{"claude_classify", "gpt_classify", "gemini_classify", "ollama_classify"}},
	{"slå upp ett bolag på organisationsnummer", []string{"roaring_company_overview", "roaring_company_search"}},
	{"ta bort dubbletterna", []string{"dedupe_rows"}},
	{"vänta på godkännande", []string{"await_approval"}},
	{"gruppera och räkna antal", []string{"group_aggregate"}},
	{"en påminnelse till telefonen", []string{"ntfy"}},
	{"hämta en fil från servern", []string{"sftp_download_file", "http_download", "drive_download"}},
	{"skapa ett möte i kalendern", []string{"gcal_create_event", "caldav_create_event"}},
	{"fördröj nästa steg", []string{"delay"}},
	{"kryptera med en checksumma", []string{"hash"}},
	{"spårning av paketet", []string{"nshift_get_shipment"}},
	{"prenumerera på ett nyhetsflöde", []string{"rss"}},
}

// Swedish words that carry no intent must score nothing at all. A hit here is
// an accident of spelling inside an English id, not a match.
var swedishNoise = []string{
	"min", "mina", "mitt", "en", "ett", "och", "att", "för", "som", "den",
	"det", "till", "från", "med", "vad", "när", "hur", "varje", "någon",
}

// A SHORT query is the other half of what the Swedish-noise guard covers, and
// the half it deliberately leaves out: "ai" and "db" mean something, so they
// cannot be answered by steps that merely contain the letters. Both used to
// be — `matchScore` scored a bare substring of the id at +100 and the label at
// +50, so "ai" was answered by aw(ai)t_approval, cont(ai)ns and em(ai)l at 150
// apiece while an exact "ai" tag scored 55, and "db" was answered by the three
// transform descriptions that mention "a DB query" in passing.
func TestRetrieval_ShortQueriesMeanWhatTheySay(t *testing.T) {
	mans := allManifests()
	byID := map[string]core.Manifest{}
	for _, m := range mans {
		byID[m.ID] = m
	}
	// A step that carries the word — as its category or one of its tags — is
	// a right answer; naming exact ids here would make the test a diary of
	// today's catalogue instead of a check on the ranking.
	carries := func(m core.Manifest, word string) bool {
		if strings.EqualFold(m.Category, word) {
			return true
		}
		for _, tag := range m.Tags {
			if strings.EqualFold(tag, word) {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct{ query, word string }{
		{"ai", "ai"},
		{"db", "db"},
	} {
		hits := rankedIDs(mans, tc.query)
		if len(hits) == 0 {
			t.Errorf("%q found nothing", tc.query)
			continue
		}
		if !carries(byID[hits[0]], tc.word) {
			shown := hits
			if len(shown) > 4 {
				shown = shown[:4]
			}
			t.Errorf("%q ranked %q first, which does not carry the word %q (%v) — an incidental substring outranked a real match",
				tc.query, hits[0], tc.word, shown)
		}
	}

	// The substring match still has to earn its keep: neither of these is a
	// word of the id, and both are the right answer.
	for _, tc := range []struct{ query, want string }{
		{"mail", "imap_get_message"},
		{"sheet", "sheets_read_range"},
	} {
		hits := rankedIDs(mans, tc.query)
		if !slices.Contains(hits, tc.want) {
			t.Errorf("%q no longer finds %s at all — the substring tier was cut too deep", tc.query, tc.want)
		}
	}
}

func TestRetrieval_SwedishNoiseFindsNothing(t *testing.T) {
	mans := allManifests()
	for _, w := range swedishNoise {
		// Two words, so the query goes through the per-word path where the
		// stop-word list applies; a bare one-word search is a deliberate act.
		if got := rankedIDs(mans, w+" "+w); len(got) > 0 {
			shown := got
			if len(shown) > 4 {
				shown = shown[:4]
			}
			t.Errorf("the Swedish filler %q scored %d steps (%v) — it matched inside an id, it did not mean anything",
				w, len(got), shown)
		}
	}
}

// Brand and product names are the same word in both languages, so these are the
// Swedish asks that can work today. They are a floor: if a change breaks them,
// Swedish has gone from weak to unusable.
func TestRetrieval_SwedishReachesTheNordicSteps(t *testing.T) {
	mans := allManifests()
	var at5 int
	var missed []string
	for _, c := range swedishCases {
		got := rankedIDs(mans, c.ask)
		top5 := got
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		if anyOf(c.want, top5) {
			at5++
			continue
		}
		if len(top5) == 0 {
			top5 = []string{"NOTHING"}
		}
		missed = append(missed, c.ask+"  (want "+strings.Join(c.want, "/")+", got "+strings.Join(top5, ", ")+")")
	}
	n := len(swedishCases)
	t.Logf("swedish: %d asks, hit@5 %d%% (%d/%d)", n, 100*at5/n, at5, n)
	if len(missed) > 0 {
		t.Logf("missed:\n  %s", strings.Join(missed, "\n  "))
	}
	if 100*at5/n < minSwedishHitAt5 {
		t.Errorf("swedish hit@5 %d%% is below the floor of %d%%", 100*at5/n, minSwedishHitAt5)
	}
}

// An alias value must be a word the catalogue actually contains, or it is dead
// vocabulary: the Swedish word looks handled and still finds nothing. Only a
// package that can see the manifests can check this, which is why the table's
// own tests cannot.
func TestRetrieval_SwedishAliasesPointAtRealCatalogueWords(t *testing.T) {
	mans := allManifests()
	var dead []string
	for key, terms := range svsearch.Aliases {
		for _, term := range terms {
			if len(rankManifests(mans, term)) == 0 {
				dead = append(dead, key+" → "+term)
			}
		}
	}
	sort.Strings(dead)
	if len(dead) > 0 {
		t.Errorf("%d alias values match no step in the catalogue:\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
}
