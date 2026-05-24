# ap-invoice — realistic shape demo

A simulated accounts-payable pipeline. Each "invoice received" event
fires the same graph, which then routes the work based on dollar amount.
Everything runs locally against a mock backend so the demo is
self-contained.

## What gets exercised

| Feature | Where |
|---|---|
| **Webhook trigger** with per-graph secret | `POST /trigger/dev/default/process-invoice-{low,high}` |
| **http_request** with secret-injected `Authorization` | every outbound call |
| **Secret references** (`env://...`) preserved in graph JSON | audit grep at end of `run.sh` |
| **Branch** with field-path + numeric condition | `classify` node, `amount > 1000` |
| **Multi-output fan** | `fetch_invoice.response_body` feeds *both* `classify` and `archive` |
| **Dormant edges** | the not-taken branch (`notify_cfo` or `auto_approve`) records as `skipped` |
| **Per-tenant sandbox** | `file_write` puts archives in `<sandbox>/dev/default/archive/` |
| **mTLS-disabled-for-dev opt-in** | hzd dials backend over insecure HTTP because the backend isn't TLS |
| **Audit isolation** | graph JSON on disk never holds resolved secret values |

## Topology

```
              POST /trigger/.../process-invoice-{low,high}
                                  │   (Authorization: Bearer webhook-secret)
                                  ▼
                          ┌─────────────────┐
                          │  fetch_invoice  │   GET /invoices/{id}
                          │  http_request   │   Authorization: env://INVOICE_API_KEY
                          └─────┬───────────┘
                response_body   │           ↘
                                ▼              ↘
                          ┌─────────────────┐    ↘
                          │     classify    │     ↘─→ ┌─────────┐
                          │     branch      │         │ archive │
                          │ amount > 1000 ? │         │file_write│
                          └─┬───────────┬───┘         └─────────┘
                       then│           │else
                           ▼           ▼
                ┌──────────────┐ ┌─────────────────┐
                │  notify_cfo  │ │  auto_approve   │
                │ http_request │ │  http_request   │
                │ /cfo (slack) │ │ /approvals/auto │
                └──────────────┘ └─────────────────┘
```

## Run it

```
./run.sh
```

Output ends with assertions. All ten should pass:
- both graphs succeed
- the low-value invoice (\$250) ran `auto_approve`, with `notify_cfo` skipped
- the high-value invoice (\$12,500) ran `notify_cfo`, with `auto_approve` skipped
- both archive files exist on disk with the original invoice JSON
- mock backend's Authorization checks all pass — secrets reached it
- audit grep finds `env://INVOICE_API_KEY` in the saved graph, never the cleartext

## What this would need to be real

The graph and policies are honest. What's missing for an actual production AP product:

| Gap | Today | Real |
|---|---|---|
| Invoice ID source | Hardcoded in graph URL | Webhook body → graph input (TODO) |
| OCR step | Skipped (data ships fully-formed from mock) | Real OCR vendor with retry on transient failures |
| ERP integration | Mock `/approvals/auto` | Actual NetSuite / SAP / Dynamics API |
| Slack notification | Mock `/notifications/cfo` | Real Slack webhook with templated message |
| Multi-step approval | One-shot | `await_approval` node that pauses until human input (TODO) |
| Idempotency keys | None | Required to safely retry POSTs to payment systems |
| Vendor portal pull | Manually triggered | Cron + IMAP/sFTP modules (TODO) |
| Audit retention | Workspace Git history | Compliance-grade WORM storage with N-year retention |

The point of the demo isn't that this is a finished AP product. It's
that the platform's primitives — triggers, http_request with secrets,
branch, file_write, audit — compose into shapes that actually look like
real corporate workflows.

## Friction caught while building this demo

1. **Secrets are whole-string substitutions.** `Authorization: env://KEY` resolves to whatever's in the env var. So the env var must contain `Bearer <token>` if the API expects Bearer auth — not just the token. Future: template-style substitution (`Bearer ${env:KEY}`) for partial-string injection.

2. **Webhook bodies are still ignored.** The graph has no access to `POST /trigger/... <body>`. Workflows that need the inbound data (e.g. "process the invoice whose ID is in the webhook payload") must hardcode IDs or fetch a "latest" endpoint. The fix is a `webhook_input` module the engine seeds with the body.

3. **Port collisions hit demos hard.** Default :8080 collided with something on the dev box, hzd silently lost the listener thread. Picked :18080 to dodge; production deployments need explicit health-check failures when a bind fails (current behavior: log + continue, which is wrong).
