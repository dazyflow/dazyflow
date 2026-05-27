# Deploying hzd

## TLS / reverse-proxy contract

`hzd` does **not** terminate TLS. Run it behind a TLS-terminating reverse
proxy (nginx, Caddy, Traefik, a k8s ingress) and proxy plain HTTP to the
gateway port.

The proxy MUST:
- terminate TLS and forward to `hzd`'s `--http` port over HTTP;
- set `X-Forwarded-Proto: https` on forwarded requests;
- forward the `Host` and `Origin` headers unchanged (the gateway's CSRF
  origin check + CORS allowlist depend on them);
- upgrade WebSocket/SSE connections (Vite HMR in dev, the chat + run SSE
  streams in prod).

`hzd` MUST be started with:
- `--trust-proxy-headers` — so it honors `X-Forwarded-Proto` and marks
  session cookies `Secure` + sends HSTS on forwarded-HTTPS requests.
  **Do not set this if hzd is exposed directly** (a client could spoof
  the header to flip Secure on over plain HTTP).
- `--web-origin https://your.domain` — the exact browser origin, for the
  CORS allowlist + the cookie-origin CSRF check.
- `--public-base-url https://your.domain` — used for OAuth redirect URIs
  and failure-notification deep links.

What the gateway does once `--trust-proxy-headers` is on and the request
arrives as forwarded-HTTPS:
- session cookie gets `Secure` (plus the existing `HttpOnly` +
  `SameSite=Lax`);
- responses carry `Strict-Transport-Security: max-age=31536000;
  includeSubDomains` and `X-Content-Type-Options: nosniff`.

### nginx example

```nginx
server {
    listen 443 ssl;
    server_name app.example.com;

    ssl_certificate     /etc/letsencrypt/live/app.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/app.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;     # hzd --http :8080
        proxy_http_version 1.1;
        proxy_set_header Host              $host;
        proxy_set_header Origin            $http_origin;
        proxy_set_header X-Real-IP         $remote_addr;
        proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;   # required
        # SSE + WebSocket (HMR / run streams)
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 600s;
    }
}
```

Matching daemon flags:

```sh
hzd --http :8080 --web-dist /srv/web \
    --trust-proxy-headers \
    --web-origin https://app.example.com \
    --public-base-url https://app.example.com \
    --postgres-dsn "$HAZYFLOW_POSTGRES_DSN" \
    --master-key "$HAZYFLOW_MASTER_KEY"
```

## Durability

Pass `--postgres-dsn` (or `$HAZYFLOW_POSTGRES_DSN`) so jobs, API keys,
sessions, users, and encrypted secrets persist to Postgres. Without it
those run in-memory/JSON and are lost on restart (dev only — the daemon
logs a warning). Provide a stable `--master-key` (32-byte base64); losing
it makes every stored secret undecryptable.

## Security flags worth setting

- `--dev-key` defaults **off**; only enable for local dev.
- `--auth-rate-per-min` / `--auth-rate-burst` throttle sign-in/sign-up
  per IP (defaults 20 / 10).
- `--http-egress-allow` pins the `http_request` / `webhook_send` drops to
  an allowlist of hosts (`api.stripe.com`, `*.slack.com`, CIDRs). The
  IP-level SSRF guard (blocks private/loopback/metadata) is always on.
