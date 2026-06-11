package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// ListPrices enumerates the account's active prices as picker options —
// the backend for the "stripe-price" param format, so a user picks
// "Pro plan — 49.99 USD/month" from a dropdown instead of pasting a
// price_… id. Called by the daemon's resource-picker endpoint (not by a
// drop), with the api_key already resolved into the job params.
func ListPrices(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("active", "true")
	q.Set("limit", "100")
	// Expand the product so the option label can lead with its name —
	// a bare price has only a nickname, which most accounts leave unset.
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
			// Expanded object normally; falls back to the bare id string
			// when expansion is unavailable.
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

// ListSubscriptions enumerates the account's non-canceled subscriptions as
// picker options — the backend for the "stripe-subscription" param format, so
// a user picks "jane@acme.com — 4999 USD/month (active)" from a dropdown
// instead of pasting a sub_… id. Called by the daemon's resource-picker
// endpoint (not by a drop), with the api_key already resolved into the job
// params.
func ListSubscriptions(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("limit", "100")
	// Omitting `status` returns every subscription that isn't canceled — the
	// set a cancel step can actually act on (active, trialing, past_due, …).
	// Expand the customer so the label can lead with who it's for.
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
		// Lead with whoever the subscription is for — the customer's name,
		// then their email; the plan and status still identify it if neither
		// is set. The raw sub_… id is the fallback of last resort.
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

// ListPaymentIntents enumerates the account's refundable (succeeded)
// payment intents as picker options — the backend for the
// "stripe-payment-intent" param format, so a user picks "49.99 USD —
// jane@acme.com (succeeded)" from a dropdown instead of pasting a pi_… id.
// Called by the daemon's resource-picker endpoint (not by a drop), with the
// api_key already resolved into the job params.
func ListPaymentIntents(ctx context.Context, job core.Job) ([]core.AccountResource, error) {
	q := url.Values{}
	q.Set("limit", "100")
	// Expand the customer so the label can name who paid.
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
		// Only succeeded payments can be refunded — skip the rest so the
		// dropdown only offers ids the refund step can actually act on.
		if pi.Status != "succeeded" {
			continue
		}
		// Lead with the amount (the most identifying detail for a refund),
		// then who it was for: description, customer email, or receipt email.
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

// ListCustomers enumerates the account's customers as picker options — the
// backend for the "stripe-customer" param format, so a user picks "Jane Doe
// — jane@acme.com" from a dropdown instead of pasting a cus_… id. Called by
// the daemon's resource-picker endpoint (not by a drop), with the api_key
// already resolved into the job params.
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
		// Label with whatever identifies the customer: name and email
		// together, then either alone, falling back to the raw id.
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

// priceZeroDecimalCurrencies are the currencies Stripe represents in
// whole units rather than hundredths — per
// https://docs.stripe.com/currencies#zero-decimal.
var priceZeroDecimalCurrencies = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true,
	"kmf": true, "krw": true, "mga": true, "pyg": true, "rwf": true,
	"ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true,
	"xpf": true,
}

// formatPriceAmount renders a minor-unit amount as "49.99 USD" /
// "5000 JPY". Empty for metered/tiered prices, which have no
// unit_amount — the label then carries just the product name.
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
