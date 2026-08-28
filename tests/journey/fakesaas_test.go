// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package journey

// fakeSaaS is one HTTP server standing in for every outside service the
// scenario corpus talks to, plus a tiny SMTP server for the mail step.
//
// It is deliberately STATEFUL. A flow that marks a spreadsheet row done must
// see that row as done on its next run, or "nothing happens twice" is
// untestable — which is the property most worth proving and the most damaging
// to get wrong (a customer texted twice, an invoice raised twice). The sheet
// here is a real in-memory sheet: values.get returns what values:batchUpdate
// wrote.
//
// Every service records what it received, so a test asserts on what the world
// actually saw rather than on the run's own status.

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type sentMail struct {
	To      string
	Subject string
	Body    string
}

type fakeSaaS struct {
	srv  *httptest.Server
	smtp net.Listener
	t    *testing.T

	mu sync.Mutex

	// sheet is a tab name → grid, row 1 being the header row.
	sheet map[string][][]any

	// what the world received
	slack      []string
	discord    []string
	ntfy       []string
	ntfyClicks []string
	sms        []string
	mail       []sentMail
	gmail      []string // decoded RFC822
	invoices   []map[string]any
	shipments  []map[string]any
	events     []string // calendar event summaries
	uploads    []string // drive file names

	// failing services answer 5xx, for the fault-injection tests
	failing map[string]bool

	// site is what the uptime check sees
	siteStatus int
	siteBody   string

	// inbox is what a Gmail search finds, newest last. Mutable so a test
	// can deliver a new email between runs — the only way "only new since
	// last run" can actually be checked.
	inbox []fakeEmail
}

type fakeEmail struct {
	ID, Thread, From, Subject string
	EpochMS                   int64
	Sent                      bool
}

func newFakeSaaS(t *testing.T) *fakeSaaS {
	t.Helper()
	f := &fakeSaaS{
		t:          t,
		sheet:      map[string][][]any{},
		failing:    map[string]bool{},
		siteStatus: 200,
		siteBody:   "Välkommen",
	}
	mux := http.NewServeMux()

	// --- Google Sheets: a real little spreadsheet ---------------------
	mux.HandleFunc("/spreadsheets/", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "sheets") {
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/values:batchUpdate"):
			f.sheetsBatchUpdate(rw, r)
		case strings.Contains(r.URL.Path, ":append"):
			f.sheetsAppend(rw, r)
		default:
			f.sheetsGet(rw, r)
		}
	})

	// --- Gmail --------------------------------------------------------
	mux.HandleFunc("/users/me/messages/send", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "gmail") {
			return
		}
		var body struct {
			Raw string `json:"raw"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		decoded, _ := base64.RawURLEncoding.DecodeString(body.Raw)
		f.record(func() { f.gmail = append(f.gmail, string(decoded)) })
		writeJSON(rw, map[string]any{"id": "mock-msg", "labelIds": []string{"SENT"}})
	})
	mux.HandleFunc("/users/me/messages", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "gmail") {
			return
		}
		stubs := []map[string]any{}
		for _, m := range f.inboxSnapshot() {
			stubs = append(stubs, map[string]any{"id": m.ID, "threadId": m.Thread})
		}
		writeJSON(rw, map[string]any{"messages": stubs})
	})
	// Hydration for each search hit.
	mux.HandleFunc("/users/me/messages/", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "gmail") {
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
		for _, m := range f.inboxSnapshot() {
			if m.ID == id {
				writeJSON(rw, gmailMessageAt(m))
				return
			}
		}
		http.NotFound(rw, r)
	})
	mux.HandleFunc("/users/me/threads/", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "gmail") {
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/users/me/threads/")
		// t1 was answered by the customer; t2 never was.
		msgs := []map[string]any{gmailMessage("m1", id, "me@example.com", "Offert", true)}
		if id == "t1" {
			msgs = append(msgs, gmailMessage("m1b", id, "kund@example.com", "Re: Offert", false))
		}
		writeJSON(rw, map[string]any{"id": id, "messages": msgs})
	})

	// --- Google Calendar ----------------------------------------------
	mux.HandleFunc("/calendars/", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "calendar") {
			return
		}
		if r.Method == http.MethodPost {
			var ev map[string]any
			_ = json.NewDecoder(r.Body).Decode(&ev)
			summary, _ := ev["summary"].(string)
			f.record(func() { f.events = append(f.events, summary) })
			writeJSON(rw, map[string]any{"id": "ev1", "htmlLink": "https://cal.example/ev1"})
			return
		}
		writeJSON(rw, map[string]any{"items": []any{}})
	})

	// --- Google Drive ---------------------------------------------------
	mux.HandleFunc("/files", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "drive") {
			return
		}
		f.record(func() { f.uploads = append(f.uploads, r.URL.Query().Get("uploadType")) })
		writeJSON(rw, map[string]any{"id": "file1", "webViewLink": "https://drive.example/file1"})
	})

	// --- Slack / Discord / ntfy / Twilio --------------------------------
	mux.HandleFunc("/chat.postMessage", func(rw http.ResponseWriter, r *http.Request) {
		if f.downSlack(rw) {
			return
		}
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.record(func() { f.slack = append(f.slack, body.Text) })
		writeJSON(rw, map[string]any{"ok": true, "ts": "1.2", "channel": "C1"})
	})
	mux.HandleFunc("/webhooks/discord", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "discord") {
			return
		}
		var body struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.record(func() { f.discord = append(f.discord, body.Content) })
		rw.WriteHeader(204)
	})
	mux.HandleFunc("/ntfy/", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "ntfy") {
			return
		}
		msg := readBody(r)
		if title := r.Header.Get("Title"); title != "" {
			msg = title + ": " + msg
		}
		// The tap target matters as much as the text for an approval
		// notification — it's the whole point of the message.
		click := r.Header.Get("Click")
		f.record(func() {
			f.ntfy = append(f.ntfy, msg)
			f.ntfyClicks = append(f.ntfyClicks, click)
		})
		writeJSON(rw, map[string]any{"id": "n1"})
	})
	mux.HandleFunc("/2010-04-01/", func(rw http.ResponseWriter, r *http.Request) { // Twilio
		if f.down(rw, "twilio") {
			return
		}
		_ = r.ParseForm()
		f.record(func() { f.sms = append(f.sms, r.PostForm.Get("To")+": "+r.PostForm.Get("Body")) })
		writeJSON(rw, map[string]any{"sid": "SM1", "status": "queued"})
	})

	// --- Fortnox --------------------------------------------------------
	mux.HandleFunc("/invoices", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "fortnox") {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.record(func() { f.invoices = append(f.invoices, body) })
		// Fortnox returns the document number as a string.
		writeJSON(rw, map[string]any{"Invoice": map[string]any{
			"DocumentNumber": strconv.Itoa(len(f.invoices) + 100),
			"CustomerNumber": "1001",
		}})
	})

	// --- nShift (book a consignment) -------------------------------------
	mux.HandleFunc("/rs-extapi/v1/shipments", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "nshift") {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		n := len(f.shipments) + 1
		f.shipments = append(f.shipments, body)
		f.mu.Unlock()
		writeJSON(rw, map[string]any{
			"id": fmt.Sprintf("SHIP-%d", n),
			"parcels": []map[string]any{
				{"parcelNo": fmt.Sprintf("PN%d", n), "copyNo": fmt.Sprintf("TRACK-%d", n)},
			},
		})
	})

	// --- 46elks (SMS) -----------------------------------------------------
	mux.HandleFunc("/sms", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "elks") {
			return
		}
		_ = r.ParseForm()
		f.record(func() {
			f.sms = append(f.sms, r.PostForm.Get("to")+": "+r.PostForm.Get("message"))
		})
		writeJSON(rw, map[string]any{"id": "smsid1", "status": "created", "to": r.PostForm.Get("to")})
	})

	// --- Claude ---------------------------------------------------------
	mux.HandleFunc("/v1/messages", func(rw http.ResponseWriter, r *http.Request) {
		if f.down(rw, "claude") {
			return
		}
		var req struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		raw := readBody(r)
		_ = json.Unmarshal([]byte(raw), &req)

		// A forced tool (classify, extract, …) must be answered with a
		// tool_use block — a text reply is what the drop reports as "the
		// model did not return a category".
		if len(req.Tools) > 0 {
			writeJSON(rw, map[string]any{
				"id": "msg_mock", "type": "message", "role": "assistant",
				"content": []map[string]any{{
					"type": "tool_use", "id": "tu1", "name": req.Tools[0].Name,
					"input": f.toolAnswer(req.Tools[0].Name, userText(req.Messages)),
				}},
				"model": "mock", "stop_reason": "tool_use",
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
			return
		}
		writeJSON(rw, map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant",
			"content":     []map[string]any{{"type": "text", "text": "Sammanfattning: " + truncateForMock(raw)}},
			"model":       "mock",
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	})

	// --- the site the uptime check watches -------------------------------
	mux.HandleFunc("/site", func(rw http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		status, body := f.siteStatus, f.siteBody
		f.mu.Unlock()
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	})

	f.srv = httptest.NewServer(mux)
	f.startSMTP()
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSaaS) Close() {
	if f.srv != nil {
		f.srv.Close()
	}
	if f.smtp != nil {
		_ = f.smtp.Close()
	}
}

// URL is the base every connector is pointed at.
func (f *fakeSaaS) URL() string { return f.srv.URL }

func (f *fakeSaaS) record(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// fail makes one service answer 500 until cleared — the fault injection the
// degradation tests need.
func (f *fakeSaaS) fail(service string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failing[service] = down
}

func (f *fakeSaaS) down(rw http.ResponseWriter, service string) bool {
	f.mu.Lock()
	bad := f.failing[service]
	f.mu.Unlock()
	if bad {
		rw.WriteHeader(http.StatusInternalServerError)
		_, _ = rw.Write([]byte(`{"error":{"message":"` + service + ` is having a bad day"}}`))
	}
	return bad
}

// Slack answers 200 with ok:false when it fails — the shape its API really
// uses, so the drop's own error handling is what's exercised.
func (f *fakeSaaS) downSlack(rw http.ResponseWriter) bool {
	f.mu.Lock()
	bad := f.failing["slack"]
	f.mu.Unlock()
	if bad {
		writeJSON(rw, map[string]any{"ok": false, "error": "channel_not_found"})
	}
	return bad
}

// deliver adds an email to what a search will find, newer than everything
// already there.
func (f *fakeSaaS) deliver(id, thread, from, subject string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var newest int64 = 1750000000000
	for _, m := range f.inbox {
		if m.EpochMS >= newest {
			newest = m.EpochMS + 60000
		}
	}
	f.inbox = append(f.inbox, fakeEmail{ID: id, Thread: thread, From: from, Subject: subject, EpochMS: newest})
}

func (f *fakeSaaS) inboxSnapshot() []fakeEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeEmail(nil), f.inbox...)
}

func (f *fakeSaaS) setSite(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.siteStatus, f.siteBody = status, body
}

// userText is what the person actually wrote. Judging on the whole request
// would be judging the SYSTEM prompt too — and a spam category whose
// description says "SEO, crypto" makes every enquiry look like spam.
func userText(messages []struct {
	Content any `json:"content"`
}) string {
	var b strings.Builder
	for _, m := range messages {
		switch c := m.Content.(type) {
		case string:
			b.WriteString(c + "\n")
		case []any:
			for _, part := range c {
				if pm, ok := part.(map[string]any); ok {
					if txt, ok := pm["text"].(string); ok {
						b.WriteString(txt + "\n")
					}
				}
			}
		}
	}
	return b.String()
}

// toolAnswer is the stand-in model's judgement. It reads what the person
// wrote rather than being scripted per test, so a test states the input (a
// spammy enquiry) and not the answer.
func (f *fakeSaaS) toolAnswer(tool, prompt string) map[string]any {
	low := strings.ToLower(prompt)
	switch tool {
	case "classify":
		for _, marker := range []string{"seo", "crypto", "link building", "backlink"} {
			if strings.Contains(low, marker) {
				return map[string]any{"category": "spam", "confidence": 0.95}
			}
		}
		if strings.Contains(low, "unhappy") || strings.Contains(low, "besviken") {
			return map[string]any{"category": "unhappy", "confidence": 0.9}
		}
		return map[string]any{"category": "genuine", "confidence": 0.9}
	case "extract":
		return map[string]any{"fields": map[string]any{}}
	}
	return map[string]any{}
}

func truncateForMock(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

// --- the in-memory spreadsheet -------------------------------------------

// putSheet seeds a tab. Row 1 is the header row, as in a real sheet.
func (f *fakeSaaS) putSheet(tab string, grid [][]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sheet[tab] = grid
}

func (f *fakeSaaS) getSheet(tab string) [][]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]any, len(f.sheet[tab]))
	for i, row := range f.sheet[tab] {
		out[i] = append([]any(nil), row...)
	}
	return out
}

// cell reads one cell by header name, 1-based sheet row.
func (f *fakeSaaS) cell(tab, header string, row int) string {
	grid := f.getSheet(tab)
	if len(grid) == 0 || row < 1 || row > len(grid) {
		return ""
	}
	for i, h := range grid[0] {
		if fmt.Sprint(h) == header && i < len(grid[row-1]) {
			return fmt.Sprint(grid[row-1][i])
		}
	}
	return ""
}

// tabFromRange pulls the tab name out of an A1 range ('Jobs'!C5 → Jobs).
func tabFromRange(rng string) string {
	rng = strings.TrimPrefix(rng, "'")
	if i := strings.Index(rng, "'!"); i >= 0 {
		return rng[:i]
	}
	if i := strings.Index(rng, "!"); i >= 0 {
		return strings.Trim(rng[:i], "'")
	}
	return rng
}

func (f *fakeSaaS) sheetsGet(rw http.ResponseWriter, r *http.Request) {
	// .../values/<range>
	i := strings.LastIndex(r.URL.Path, "/values/")
	rng := ""
	if i >= 0 {
		rng = r.URL.Path[i+len("/values/"):]
	}
	tab := tabFromRange(unescape(rng))
	grid := f.getSheet(tab)
	if grid == nil {
		grid = [][]any{}
	}
	writeJSON(rw, map[string]any{"range": tab, "majorDimension": "ROWS", "values": grid})
}

func (f *fakeSaaS) sheetsAppend(rw http.ResponseWriter, r *http.Request) {
	i := strings.LastIndex(r.URL.Path, "/values/")
	rng := ""
	if i >= 0 {
		rng = strings.TrimSuffix(r.URL.Path[i+len("/values/"):], ":append")
	}
	tab := tabFromRange(unescape(rng))
	var body struct {
		Values [][]any `json:"values"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.sheet[tab] = append(f.sheet[tab], body.Values...)
	n := len(body.Values)
	f.mu.Unlock()
	writeJSON(rw, map[string]any{"updates": map[string]any{"updatedRows": n}})
}

// sheetsBatchUpdate applies the per-cell writes Update cells sends, so a
// later read sees them — the whole point of the round trip.
func (f *fakeSaaS) sheetsBatchUpdate(rw http.ResponseWriter, r *http.Request) {
	var body struct {
		Data []struct {
			Range  string  `json:"range"`
			Values [][]any `json:"values"`
		} `json:"data"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	updated := 0
	f.mu.Lock()
	for _, d := range body.Data {
		tab := tabFromRange(d.Range)
		col, row, ok := parseA1(d.Range)
		if !ok || len(d.Values) == 0 || len(d.Values[0]) == 0 {
			continue
		}
		grid := f.sheet[tab]
		for len(grid) < row {
			grid = append(grid, []any{})
		}
		for len(grid[row-1]) <= col {
			grid[row-1] = append(grid[row-1], "")
		}
		grid[row-1][col] = d.Values[0][0]
		f.sheet[tab] = grid
		updated++
	}
	f.mu.Unlock()
	writeJSON(rw, map[string]any{"totalUpdatedCells": updated})
}

// parseA1 turns 'Jobs'!C5 into a zero-based column and a 1-based row.
func parseA1(rng string) (col, row int, ok bool) {
	if i := strings.Index(rng, "!"); i >= 0 {
		rng = rng[i+1:]
	}
	letters, digits := "", ""
	for _, c := range rng {
		switch {
		case c >= 'A' && c <= 'Z':
			letters += string(c)
		case c >= '0' && c <= '9':
			digits += string(c)
		}
	}
	if letters == "" || digits == "" {
		return 0, 0, false
	}
	for _, c := range letters {
		col = col*26 + int(c-'A') + 1
	}
	n, err := strconv.Atoi(digits)
	if err != nil {
		return 0, 0, false
	}
	return col - 1, n, true
}

func unescape(s string) string {
	s = strings.ReplaceAll(s, "%27", "'")
	s = strings.ReplaceAll(s, "%21", "!")
	s = strings.ReplaceAll(s, "%20", " ")
	return s
}

// --- accessors ------------------------------------------------------------

func (f *fakeSaaS) slackPosts() []string   { return f.snapshot(func() []string { return f.slack }) }
func (f *fakeSaaS) discordPosts() []string { return f.snapshot(func() []string { return f.discord }) }
func (f *fakeSaaS) pushes() []string       { return f.snapshot(func() []string { return f.ntfy }) }
func (f *fakeSaaS) texts() []string        { return f.snapshot(func() []string { return f.sms }) }
func (f *fakeSaaS) gmailSent() []string    { return f.snapshot(func() []string { return f.gmail }) }
func (f *fakeSaaS) calendarEvents() []string {
	return f.snapshot(func() []string { return f.events })
}

func (f *fakeSaaS) snapshot(get func() []string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	src := get()
	return append([]string(nil), src...)
}

// pushLinks are the tap targets of the notifications, in the same order as
// pushes(). For an approval notification the link IS the message.
func (f *fakeSaaS) pushLinks() []string {
	return f.snapshot(func() []string { return f.ntfyClicks })
}

func (f *fakeSaaS) mails() []sentMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMail(nil), f.mail...)
}

func (f *fakeSaaS) shipmentsBooked() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.shipments...)
}

func (f *fakeSaaS) invoicesRaised() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.invoices...)
}

// --- a minimal SMTP server, for the Email step ---------------------------

func (f *fakeSaaS) startSMTP() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatalf("smtp listen: %v", err)
	}
	f.smtp = ln
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go f.serveSMTP(conn)
		}
	}()
}

// serveSMTP speaks just enough of the protocol for the mail step: greet,
// accept the envelope, take the DATA blob, and record it.
func (f *fakeSaaS) serveSMTP(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	say := func(s string) {
		_, _ = w.WriteString(s + "\r\n")
		_ = w.Flush()
	}
	say("220 fake ESMTP")

	var to, subject string
	var body strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake")
			say("250 OK")
		case strings.HasPrefix(cmd, "MAIL FROM"):
			say("250 OK")
		case strings.HasPrefix(cmd, "RCPT TO"):
			if i := strings.Index(line, "<"); i >= 0 {
				if j := strings.Index(line[i:], ">"); j > 0 {
					to = line[i+1 : i+j]
				}
			}
			say("250 OK")
		case cmd == "DATA":
			say("354 send it")
			for {
				dl, derr := r.ReadString('\n')
				if derr != nil {
					return
				}
				if strings.TrimSpace(dl) == "." {
					break
				}
				if strings.HasPrefix(dl, "Subject: ") {
					subject = strings.TrimSpace(strings.TrimPrefix(dl, "Subject: "))
				}
				body.WriteString(dl)
			}
			f.record(func() { f.mail = append(f.mail, sentMail{To: to, Subject: subject, Body: body.String()}) })
			body.Reset()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		case cmd == "RSET":
			say("250 OK")
		default:
			say("250 OK")
		}
	}
}

// smtpHostPort is what the Email step's connection fields are pointed at.
func (f *fakeSaaS) smtpHostPort() (string, int) {
	addr := f.smtp.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port
}

// --- helpers --------------------------------------------------------------

// gmailMessageAt renders an inbox entry with its own timestamp, which is what
// the "only new since last run" watermark compares against.
func gmailMessageAt(m fakeEmail) map[string]any {
	msg := gmailMessage(m.ID, m.Thread, m.From, m.Subject, m.Sent)
	msg["internalDate"] = strconv.FormatInt(m.EpochMS, 10)
	return msg
}

func gmailMessage(id, threadID, from, subject string, sent bool) map[string]any {
	labels := []any{"INBOX"}
	if sent {
		labels = []any{"SENT"}
	}
	return map[string]any{
		"id": id, "threadId": threadID, "labelIds": labels,
		"internalDate": "1750000000000",
		"snippet":      subject,
		"payload": map[string]any{
			"mimeType": "text/plain",
			"headers": []any{
				map[string]any{"name": "From", "value": from},
				map[string]any{"name": "Subject", "value": subject},
				map[string]any{"name": "Date", "value": "Mon, 17 Aug 2026 09:00:00 +0200"},
			},
			"body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("hej"))},
		},
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
