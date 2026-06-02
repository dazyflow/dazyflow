package journey

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// timeShortDaysAgo returns an ISO date (YYYY-MM-DD) n days before now,
// for seeding "recent" rows that fall inside a last-week filter.
func timeShortDaysAgo(n int) string {
	return time.Now().UTC().AddDate(0, 0, -n).Format("2006-01-02")
}

// TestJourney_OverdueInvoice_RunsWithConnectedAccounts proves the
// connected happy path: a user with Google connected publishes the
// "chase overdue invoices" flow, it runs on demand, reads the invoice
// sheet, and emails exactly the one client who is overdue and unpaid.
//
// Gmail and Sheets are mocked at their HTTP base (the same SetHTTPBase
// seam the integration tests use); a raw `token` param on each node
// stands in for a connected account, so no OAuth dance is needed. This
// is the execution counterpart to the catalog/journey tests: it watches
// a SaaS scenario actually run, not just validate.
func TestJourney_OverdueInvoice_RunsWithConnectedAccounts(t *testing.T) {
	google := newGoogleMock(t)
	defer google.Close()

	s := newStack(t)
	me := s.signUp(t, "office@agency.example")

	raw, g := readGraph(t, "../scenarios/01-overdue-invoice-chaser.json")

	// The Sheets + Gmail nodes are scripted connectors; point each at the mock
	// via the base_url override (the scripted analog of the old SetHTTPBase),
	// with a raw token standing in for a connected account.
	mock := map[string]any{"token": "mock-token", "base_url": google.srv.URL}
	patchParams(&g, "read_invoices", mock)
	patchParams(&g, "log_reminded", mock)
	patchStepParams(&g, "send_reminders", mock)
	raw = fillBlanks(mustJSON(t, g))

	const flowID = "overdue-invoice-chaser"
	if r := me.saveFlow(flowID, raw); r.status != 200 {
		t.Fatalf("could not save the flow: status=%d body=%s", r.status, r.body)
	}
	if v := me.validateFlow(flowID); !v.OK {
		t.Fatalf("app would not call the flow ready: %s", issuesJSON(v))
	}

	runID := me.runFlow(flowID)
	if status := me.waitForRun(runID); status != "succeeded" {
		// Surface the failing node so the cause is obvious (this is how
		// we caught that CEL filters had no `now` to compute overdue days).
		t.Fatalf("the connected run did not succeed: status=%q\nnode failures:\n%s",
			status, me.failedNodeReport(runID))
	}

	// Exactly one reminder, to the overdue+unpaid client, with their
	// invoice number in it. The paid client must not be emailed.
	sent := google.sentEmails()
	if len(sent) != 1 {
		t.Fatalf("expected exactly 1 reminder email, got %d: %+v\nrun nodes:\n%s", len(sent), sent, me.dumpRun(runID))
	}
	if !strings.Contains(sent[0], "ada@acme.example") {
		t.Errorf("reminder did not go to the overdue client; email was:\n%s", sent[0])
	}
	if !strings.Contains(sent[0], "INV-1001") {
		t.Errorf("reminder did not reference the overdue invoice; email was:\n%s", sent[0])
	}
}

// TestJourney_WeeklySalesSummary_RunsWithConnectedAccounts proves the
// "scheduled report to Slack" shape end to end: read last week's orders
// from a Sheet, filter to the last 7 days (using the `now` the CEL env
// now exposes), total by salesperson, have the AI write the recap, and
// post it to Slack. Sheets/Claude/Slack are mocked; the AI mock echoes
// the aggregated rows it was handed so the test can confirm the data
// actually flowed through filter -> aggregate -> sort -> AI -> Slack.
func TestJourney_WeeklySalesSummary_RunsWithConnectedAccounts(t *testing.T) {
	m := newSalesMock(t)
	defer m.Close()

	s := newStack(t)
	me := s.signUp(t, "founder@shop.example")

	_, g := readGraph(t, "../scenarios/02-weekly-sales-summary.json")
	// All three connectors (Sheets, Claude, Slack) are scripted; point each at
	// the shared mock via base_url (Claude already used it).
	patchParams(&g, "read_orders", map[string]any{"token": "mock-token", "base_url": m.srv.URL})
	patchParams(&g, "compose", map[string]any{"api_key": "mock-key", "base_url": m.srv.URL})
	patchParams(&g, "post", map[string]any{"token": "mock-token", "base_url": m.srv.URL})
	raw := fillBlanks(mustJSON(t, g))

	const flowID = "weekly-sales-summary"
	if r := me.saveFlow(flowID, raw); r.status != 200 {
		t.Fatalf("could not save the flow: status=%d body=%s", r.status, r.body)
	}
	if v := me.validateFlow(flowID); !v.OK {
		t.Fatalf("app would not call the flow ready: %s", issuesJSON(v))
	}

	runID := me.runFlow(flowID)
	if status := me.waitForRun(runID); status != "succeeded" {
		t.Fatalf("the connected run did not succeed: status=%q\nnode failures:\n%s\nnodes:\n%s",
			status, me.failedNodeReport(runID), me.dumpRun(runID))
	}

	posts := m.slackPosts()
	if len(posts) != 1 {
		t.Fatalf("expected exactly 1 Slack post, got %d: %+v\nnodes:\n%s", len(posts), posts, me.dumpRun(runID))
	}
	// The AI mock echoes the rows it received, so a correct run posts the
	// top salesperson and a weekly total to Slack. "old" orders outside
	// the 7-day window must not be counted.
	if !strings.Contains(posts[0], "Sasha") {
		t.Errorf("Slack recap is missing the top salesperson; posted:\n%s", posts[0])
	}
	if strings.Contains(posts[0], "ancient-deal") {
		t.Errorf("Slack recap counted an order outside the last-week window; posted:\n%s", posts[0])
	}
}

// --- Sales mock (Sheets orders + Claude + Slack) ---------------------

type salesMock struct {
	srv   *httptest.Server
	mu    sync.Mutex
	posts []string
}

func newSalesMock(t *testing.T) *salesMock {
	t.Helper()
	m := &salesMock{}
	mux := http.NewServeMux()

	// Orders sheet: three recent orders (two salespeople) inside the
	// 7-day window, plus one ancient order that must be filtered out.
	recent := timeShortDaysAgo(2)
	older := timeShortDaysAgo(1)
	ancient := "2020-01-01"
	mux.HandleFunc("/spreadsheets/", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, map[string]any{
			"range": "Orders", "majorDimension": "ROWS",
			"values": [][]any{
				{"order_date", "salesperson", "product", "amount"},
				{recent, "Sasha", "Cake", "300"},
				{older, "Sasha", "Bread", "120"},
				{older, "Wren", "Buns", "90"},
				{ancient, "Wren", "ancient-deal", "9999"},
			},
		})
	})

	// Claude: echo the rows it was given back as the "summary" text, so
	// the downstream Slack message carries the aggregated data.
	mux.HandleFunc("/v1/messages", func(rw http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		echoed := ""
		if len(req.Messages) > 0 {
			echoed = req.Messages[len(req.Messages)-1].Content
		}
		writeJSON(rw, map[string]any{
			"id": "msg_mock", "type": "message", "role": "assistant",
			"content":     []map[string]any{{"type": "text", "text": "Weekly recap: " + echoed}},
			"model":       "mock",
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
		})
	})

	// Slack chat.postMessage: capture the posted text.
	mux.HandleFunc("/chat.postMessage", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.posts = append(m.posts, body.Text)
		m.mu.Unlock()
		writeJSON(rw, map[string]any{"ok": true, "ts": "123.456", "channel": "C1"})
	})

	m.srv = httptest.NewServer(mux)
	return m
}

func (m *salesMock) Close() { m.srv.Close() }

func (m *salesMock) slackPosts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.posts))
	copy(out, m.posts)
	return out
}

// --- Google (Sheets + Gmail) mock ------------------------------------

type googleMock struct {
	srv  *httptest.Server
	mu   sync.Mutex
	sent []string // decoded RFC822 of each Gmail send
}

func newGoogleMock(t *testing.T) *googleMock {
	t.Helper()
	m := &googleMock{}
	mux := http.NewServeMux()

	// Sheets values: read returns the invoice table; append (path ends
	// :append) just acknowledges.
	mux.HandleFunc("/spreadsheets/", func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":append") {
			writeJSON(rw, map[string]any{"updates": map[string]any{"updatedRows": 1}})
			return
		}
		// A small invoices table: one overdue+unpaid, one paid. due_date
		// is well in the past so days_overdue is positive once computed.
		writeJSON(rw, map[string]any{
			"range":          "Invoices",
			"majorDimension": "ROWS",
			"values": [][]any{
				{"invoice_no", "client_name", "client_email", "amount", "due_date", "status"},
				{"INV-1001", "Acme Ltd", "ada@acme.example", "1200", "2020-01-01", "unpaid"},
				{"INV-1002", "Globex", "gil@globex.example", "500", "2020-01-01", "paid"},
			},
		})
	})

	// Gmail send: capture the decoded message so the test can assert
	// who got emailed.
	mux.HandleFunc("/users/me/messages/send", func(rw http.ResponseWriter, r *http.Request) {
		var body struct {
			Raw string `json:"raw"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		decoded, _ := base64.RawURLEncoding.DecodeString(body.Raw)
		m.mu.Lock()
		m.sent = append(m.sent, string(decoded))
		m.mu.Unlock()
		writeJSON(rw, map[string]any{"id": "mock-msg", "labelIds": []string{"SENT"}})
	})

	m.srv = httptest.NewServer(mux)
	return m
}

func (m *googleMock) Close() { m.srv.Close() }

func (m *googleMock) sentEmails() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.sent))
	copy(out, m.sent)
	return out
}

func writeJSON(rw http.ResponseWriter, v any) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(v)
}

// --- graph patching --------------------------------------------------

func patchParams(g *core.Graph, nodeID string, set map[string]any) {
	for i := range g.Nodes {
		if g.Nodes[i].ID != nodeID {
			continue
		}
		if g.Nodes[i].Params == nil {
			g.Nodes[i].Params = map[string]any{}
		}
		maps.Copy(g.Nodes[i].Params, set)
	}
}

func patchStepParams(g *core.Graph, forEachNodeID string, set map[string]any) {
	for i := range g.Nodes {
		if g.Nodes[i].ID != forEachNodeID {
			continue
		}
		sp, _ := g.Nodes[i].Params["step_params"].(map[string]any)
		if sp == nil {
			sp = map[string]any{}
		}
		maps.Copy(sp, set)
		g.Nodes[i].Params["step_params"] = sp
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	return b
}
