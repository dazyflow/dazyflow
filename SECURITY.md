# Security operations

## Reporting a vulnerability

**Do not open a public issue, pull request, or discussion.** Report privately
through GitHub's **Private Vulnerability Reporting**: the repository's
**Security** tab → **Report a vulnerability**. That is the same mechanism that
later issues the advisory and requests a CVE, so a report filed there needs no
re-filing once a fix lands.

Please include:

- the **version or commit**. It is stamped into the binary and shown in the
  `build` block of `GET /api/v1` and in the web UI's account menu;
- a **reproduction** — the smallest flow, request, or configuration that shows
  the behaviour;
- the **impact you observed** and the configuration it required. Whether an
  issue is reachable in a supported configuration is what starts the
  remediation clock.

Acknowledgement and fix windows are in [docs/SECURITY-SLA.md](docs/SECURITY-SLA.md).
The clock starts when the issue is confirmed reachable, not when it is received.

Everything below is the **operator's** runbook for the master key. It is not a
disclosure channel.

## The master key (`$DAZYFLOW_MASTER_KEY`)

dzd's built-in secret store (`EncryptedSecrets`, `daemon/encrypted_secrets.go`)
uses envelope encryption:

- **KEK** = the master key. A 32-byte AES-256 key you provide. Held only in
  process memory; dzd never writes it to disk.
- **DEK** = one per tenant, generated on that tenant's first secret write, then
  wrapped by the KEK and stored. Each secret is sealed with AES-256-GCM under
  its tenant's DEK.

So the master key protects every tenant's DEK, which protects every secret.

Each ciphertext is additionally **bound to the row it lives in** through
AES-GCM's additional authenticated data — `(tenant, name)` for a secret,
`tenant` for a wrapped DEK. Without that binding, GCM proves only that a blob
was sealed under the tenant's DEK, not that it was sealed under *this name*, so
anyone with database write access could relocate a ciphertext (copy
`conn.stripe.api_key`'s blob into a secret a flow may read) and recover the
plaintext through an ordinary `${secret.…}` reference.

Ciphertext written before binding existed still decrypts — dzd falls back to an
unbound open — and upgrades to the bound form on the next write. Every `Put`
re-seals, so an OAuth refresh or a re-saved connection upgrades itself, and
rotation upgrades every DEK it touches. Re-save your secrets to get the
guarantee everywhere immediately.

### Generating and storing it

```sh
openssl rand -base64 32        # dzd validates it decodes to exactly 32 bytes
```

- **Never** commit it, bake it into an image, or put it in the graph store.
  Inject it at runtime via `$DAZYFLOW_MASTER_KEY`.
- Source it from a real secret manager: AWS Secrets Manager / SSM SecureString,
  GCP Secret Manager, `vault kv get`, or a k8s `Secret` (ideally itself backed by
  one of those via the External Secrets Operator).
- Restrict read access to the same blast radius as the database — KEK + DB
  together = plaintext secrets.

**If it's lost there is no recovery.** The wrapped DEKs can't be unwrapped, so
every stored secret is undecryptable and every tenant must re-enter every
secret. Keep a sealed backup wherever your org keeps break-glass credentials.

**If it's compromised**, rotate the key *and* rotate the underlying credentials
(the Slack/GitHub/DB tokens) — re-wrapping under a new KEK doesn't help if the
plaintext already leaked.

## Master-key rotation

Rotating the KEK re-wraps every tenant's DEK under the new key. The secret
ciphertexts are untouched, so rotation is fast and needs no secret re-entry.
(`EncryptedSecrets.RewrapDEKs`; the DEK plaintexts never leave the process.)

1. Generate `NEW_KEY` (`openssl rand -base64 32`).
2. Run the re-wrap against the same store the daemon uses. The **current** key
   comes from the environment; the new one is the flag argument:

   ```sh
   DAZYFLOW_POSTGRES_DSN="$DSN" \
   DAZYFLOW_MASTER_KEY="$OLD_KEY" \
       dzd --rotate-master-key "$NEW_KEY"
   # logs: "master-key rotation complete: N DEK(s) re-wrapped, …"
   ```

   It is re-runnable — a DEK already on the new key is skipped, not
   double-wrapped — and fails loudly, leaving the store untouched, if
   `DAZYFLOW_MASTER_KEY` isn't the key that wrapped the existing DEKs.
3. Swap `$DAZYFLOW_MASTER_KEY` to `NEW_KEY` and restart the daemon.
4. Verify reads succeed, then destroy the old key.

> ⚠️ Keep the old key until step 4 verifies. If rotation is interrupted, re-run
> step 2 with the same keys before restarting.

## Related hardening

The full list is in [docs/DEPLOY.md](docs/DEPLOY.md); all of these are env vars
in `.env` (see `.env.example`):

- Run behind a TLS-terminating reverse proxy and set
  `DAZYFLOW_TRUST_PROXY_HEADERS=1`, so session cookies are `Secure` and HSTS is
  sent.
- `DAZYFLOW_DEV_KEY` defaults off; never set it in production — it mints an
  insecure bearer token at every boot.
- Secrets live only in the per-tenant encrypted store and are referenced from
  flows as `${secret.NAME}`; the daemon's process environment is never reachable
  from a flow.
- The auth rate limiter is fixed at 20/min per IP with a burst of 10
  (`ipRateLimiter` in `daemon/ratelimit.go`).
- `DAZYFLOW_HTTP_EGRESS_ALLOW` pins the `http_request` / `webhook_send` drops to
  an allowlist; the IP-level SSRF guard (private, loopback, cloud metadata) is
  always on. Both go through `EgressAllowed` in `drops/net/egress.go`.
- `DAZYFLOW_POSTGRES_DSN` for durable, restart-surviving stores.
