// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command mock-backend stands in for the third-party services a real AP
// pipeline would call: an invoice lookup API, an OCR extractor, a Slack
// CFO-approval channel, and an auto-approve endpoint. Each route logs
// what it received so the demo's tail of activity tells the full story.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	expectedInvoiceKey  = "invoice-svc-key-abc"
	expectedSlackToken  = "slack-bot-token-def"
	expectedApprovalKey = "approval-system-key-ghi"
)

func main() {
	listen := flag.String("listen", ":60500", "listen address")
	checkAuth := flag.Bool("check-auth", true, "verify expected Authorization headers")
	flag.Parse()

	http.HandleFunc("/invoices/", func(w http.ResponseWriter, r *http.Request) {
		if *checkAuth && !checkBearer(r, expectedInvoiceKey) {
			refuse(w, "invoices", r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/invoices/")
		amount := 250.0
		vendor := "Acme Industries"
		if strings.HasPrefix(id, "big") {
			amount = 12500.50
			vendor = "Megacorp Holdings"
		}
		log.Printf("→ GET /invoices/%s  → amount=%.2f (vendor=%s)", id, amount, vendor)
		respondJSON(w, 200, map[string]any{
			"id":       id,
			"vendor":   vendor,
			"amount":   amount,
			"currency": "USD",
			"date":     time.Now().Format("2006-01-02"),
		})
	})

	http.HandleFunc("/approvals/auto", func(w http.ResponseWriter, r *http.Request) {
		if *checkAuth && !checkBearer(r, expectedApprovalKey) {
			refuse(w, "approvals", r)
			return
		}
		body := readBody(r)
		log.Printf("→ POST /approvals/auto  ← %s", body)
		respondJSON(w, 200, map[string]any{
			"approved": true,
			"method":   "auto",
			"ref":      fmt.Sprintf("AP-%d", time.Now().UnixNano()%100000),
		})
	})

	http.HandleFunc("/notifications/cfo", func(w http.ResponseWriter, r *http.Request) {
		if *checkAuth && !checkBearer(r, expectedSlackToken) {
			refuse(w, "notifications", r)
			return
		}
		body := readBody(r)
		log.Printf("→ POST /notifications/cfo  ← %s", body)
		respondJSON(w, 200, map[string]any{
			"notified": true,
			"channel":  "#cfo-approvals",
		})
	})

	log.Printf("mock backend listening on %s (check-auth=%v)", *listen, *checkAuth)
	if err := http.ListenAndServe(*listen, nil); err != nil {
		log.Fatal(err)
	}
}

func checkBearer(r *http.Request, expected string) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	return strings.TrimSpace(h[len(prefix):]) == expected
}

func refuse(w http.ResponseWriter, route string, r *http.Request) {
	log.Printf("✗ %s: bad Authorization (got %q)", route, r.Header.Get("Authorization"))
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("unauthorized"))
}

func respondJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	return string(b)
}
