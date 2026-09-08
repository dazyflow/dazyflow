// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

func ListPrices(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("limit", "100")
	q.Set("expand[]", "data.product")
	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/prices?"+q.Encode(), "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("stripe returned %d: %s", status, extractStripeError(body))
	}
	var parsed struct {
		Data []struct {
			ID         string `json:"id"`
			Nickname   string `json:"nickname"`
			UnitAmount int64  `json:"unit_amount"`
			Currency   string `json:"currency"`
			Recurring  *struct {
				Interval string `json:"interval"`
			} `json:"recurring"`
			Product json.RawMessage `json:"product"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse prices: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Data))
	for _, p := range parsed.Data {
		var prod struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(p.Product, &prod)
		name := prod.Name
		if p.Nickname != "" {
			if name != "" {
				name += " / " + p.Nickname
			} else {
				name = p.Nickname
			}
		}
		if name == "" {
			name = p.ID
		}
		if amount := formatPriceAmount(p.UnitAmount, p.Currency); amount != "" {
			name += " — " + amount
			if p.Recurring != nil && p.Recurring.Interval != "" {
				name += "/" + p.Recurring.Interval
			}
		}
		out = append(out, core.AccountResource{ID: p.ID, Name: name})
	}
	return out, nil
}

func ListSubscriptions(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("limit", "100")
	q.Set("expand[]", "data.customer")
	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/subscriptions?"+q.Encode(), "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("stripe returned %d: %s", status, extractStripeError(body))
	}
	var parsed struct {
		Data []struct {
			ID       string          `json:"id"`
			Status   string          `json:"status"`
			Customer json.RawMessage `json:"customer"`
			Items    struct {
				Data []struct {
					Price struct {
						UnitAmount int64  `json:"unit_amount"`
						Currency   string `json:"currency"`
						Recurring  *struct {
							Interval string `json:"interval"`
						} `json:"recurring"`
					} `json:"price"`
				} `json:"data"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse subscriptions: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Data))
	for _, s := range parsed.Data {
		var cust struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		}
		_ = json.Unmarshal(s.Customer, &cust)
		name := cust.Name
		if name == "" {
			name = cust.Email
		}
		if len(s.Items.Data) > 0 {
			p := s.Items.Data[0].Price
			if amount := formatPriceAmount(p.UnitAmount, p.Currency); amount != "" {
				if name != "" {
					name += " — "
				}
				name += amount
				if p.Recurring != nil && p.Recurring.Interval != "" {
					name += "/" + p.Recurring.Interval
				}
			}
		}
		if s.Status != "" {
			if name != "" {
				name += " "
			}
			name += "(" + s.Status + ")"
		}
		if name == "" {
			name = s.ID
		}
		out = append(out, core.AccountResource{ID: s.ID, Name: name})
	}
	return out, nil
}

func ListPaymentIntents(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("limit", "100")
	q.Set("expand[]", "data.customer")
	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/payment_intents?"+q.Encode(), "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("stripe returned %d: %s", status, extractStripeError(body))
	}
	var parsed struct {
		Data []struct {
			ID           string          `json:"id"`
			Status       string          `json:"status"`
			Amount       int64           `json:"amount"`
			Currency     string          `json:"currency"`
			Description  string          `json:"description"`
			ReceiptEmail string          `json:"receipt_email"`
			Customer     json.RawMessage `json:"customer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse payment intents: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Data))
	for _, pi := range parsed.Data {
		if pi.Status != "succeeded" {
			continue
		}
		name := formatPriceAmount(pi.Amount, pi.Currency)
		var cust struct {
			Email string `json:"email"`
		}
		_ = json.Unmarshal(pi.Customer, &cust)
		who := pi.Description
		if who == "" {
			who = cust.Email
		}
		if who == "" {
			who = pi.ReceiptEmail
		}
		if who != "" {
			if name != "" {
				name += " — "
			}
			name += who
		}
		if name == "" {
			name = pi.ID
		}
		out = append(out, core.AccountResource{ID: pi.ID, Name: name})
	}
	return out, nil
}

func ListCustomers(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("limit", "100")
	status, body, err := stripeDo(ctx, job, http.MethodGet, baseURL(job)+"/customers?"+q.Encode(), "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("stripe returned %d: %s", status, extractStripeError(body))
	}
	var parsed struct {
		Data []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse customers: %w", err)
	}
	out := make([]core.AccountResource, 0, len(parsed.Data))
	for _, c := range parsed.Data {
		name := c.Name
		if c.Email != "" {
			if name != "" {
				name += " — " + c.Email
			} else {
				name = c.Email
			}
		}
		if name == "" {
			name = c.ID
		}
		out = append(out, core.AccountResource{ID: c.ID, Name: name})
	}
	return out, nil
}

var priceZeroDecimalCurrencies = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true,
	"kmf": true, "krw": true, "mga": true, "pyg": true, "rwf": true,
	"ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true,
	"xpf": true,
}

func formatPriceAmount(minor int64, currency string) string {
	if minor == 0 || currency == "" {
		return ""
	}
	code := strings.ToUpper(currency)
	if priceZeroDecimalCurrencies[strings.ToLower(currency)] {
		return fmt.Sprintf("%d %s", minor, code)
	}
	return fmt.Sprintf("%d.%02d %s", minor/100, minor%100, code)
}
