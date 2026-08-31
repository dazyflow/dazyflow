# Security operations

## Reporting a vulnerability

**Do not open a public issue, pull request, or discussion for a suspected
vulnerability.** Report it privately through GitHub's **Private Vulnerability
Reporting**: the repository's **Security** tab → **Report a vulnerability**.
That opens a channel visible only to the maintainers, and it is the same
mechanism that later issues the advisory and requests a CVE, so a report filed
there needs no re-filing once a fix lands.

Please include:

- the **version or commit** you observed it on. The running version is stamped
  into the binary at build time and shown in the `build` block of `GET /api/v1`
  and in the web UI's account menu, so you do not have to guess it;
- a **reproduction** — the smallest flow, request, or configuration that shows
  the behaviour;
- the **impact you actually observed**, and the configuration it required.
  Whether an issue is reachable in a supported configuration is what starts the
  remediation clock, so this is the most useful thing you can tell us.

Acknowledgement and fix windows are in
[docs/SECURITY-SLA.md](docs/SECURITY-SLA.md): critical is acknowledged within
24 hours and fixed within 7 days, high within 2 business days and 7 days,
medium 5 business days and 30 days, low 10 business days and 90 days. The clock
starts when the issue is confirmed reachable, not when it is received — that
document explains how an unreachable advisory is recorded and tracked instead.

Everything below this section is the **operator's** runbook: how to hold, rotate
and recover the master key that protects every tenant's secrets. It is not a
disclosure channel.

## The master key (`$DAZYFLOW_MASTER_KEY`)

dzd's built-in secret store (`EncryptedSecrets`, `daemon/encrypted_secrets.go`)
uses **envelope encryption**:

- **KEK** (Key-Encryption-Key) = the master key. A 32-byte AES-256 key
  you provide. Held only in process memory; dzd never writes it to disk.
- **DEK** (Data-Encryption-Key) = one per tenant, generated on that
  tenant's first secret write, then **wrapped (encrypted) by the KEK**
  and stored. Each secret is sealed with AES-256-GCM under its tenant's
  DEK.

So the master key protects every tenant's DEK, which protects every
secret. It is the single most sensitive piece of configuration.

Each ciphertext is additionally **bound to the row it lives in** through
AES-GCM's additional authenticated data — `(tenant, name)` for a secret,
`tenant` for a wrapped DEK. Without that binding, GCM proves only that a blob
was sealed under the tenant's DEK, not that it was sealed under *this name*, so
anyone with write access to the database could relocate a ciphertext (copy
`conn.stripe.api_key`'s blob into a secret a flow is allowed to read) and
recover the plaintext through an ordinary `${secret.…}` reference. The binding
makes that relocation fail to authenticate.

Ciphertext written before binding existed still decrypts — dzd falls back to an
unbound open — and upgrades to the bound form when the value is next written.
Every `Put` re-seals, so an OAuth refresh or a re-saved connection upgrades
itself, and the rotation below upgrades every DEK it touches. A deployment that
wants the guarantee everywhere immediately can re-save its secrets.

### Generating it

```sh
openssl rand -base64 32        # 32 bytes, base64-encoded — what dzd expects
```

dzd validates it decodes to exactly 32 bytes and exits otherwise.

### Storing it

- **Never** commit it, bake it into an image, or put it in the graph
  store. Inject it at runtime via `$DAZYFLOW_MASTER_KEY`.
- Source it from a real secret manager and export into the daemon's
  environment at boot:
  - **AWS**: `aws secretsmanager get-secret-value` / SSM Parameter Store
    (SecureString) → env in your entrypoint or task definition.
  - **GCP**: Secret Manager → env via the deployment manifest.
  - **Vault**: `vault kv get` / Agent-injected env or file.
  - **k8s**: a `Secret` mounted as an env var (ideally itself backed by
    one of the above via the External Secrets Operator).
- Restrict who can read it to the same blast radius as the database —
  KEK + DB together = plaintext secrets.

### If it's lost

There is **no recovery**. The wrapped DEKs can't be unwrapped, so every
stored secret is undecryptable. Treat master-key loss as "every tenant
must re-enter every secret." Keep a sealed backup of the key wherever
your org keeps break-glass credentials.

### If it's compromised

Assume every secret encrypted under it is exposed. Rotate the key (below)
**and** rotate the underlying credentials themselves (the Slack/GitHub/DB
tokens) — re-wrapping under a new KEK doesn't help if the plaintext
already leaked.

## Master-key rotation

Rotating the KEK re-wraps every tenant's DEK under the new key. The
secret ciphertexts (sealed under the per-tenant DEKs) are untouched —
only the wrapped-DEK rows change — so rotation is fast, low-risk, and
requires **no secret re-entry**. `dzd --rotate-master-key` does the
re-wrap; the DEK plaintexts never leave the process.
(Implementation: `EncryptedSecrets.RewrapDEKs` in
`daemon/encrypted_secrets.go`.)

### Procedure

1. Generate `NEW_KEY` (`openssl rand -base64 32`).
2. Run the re-wrap against the same store the daemon uses. The
   **current** key comes from `$DAZYFLOW_MASTER_KEY`; the new one is
   passed as the `--rotate-master-key` flag (one of only two flags
   `dzd` still accepts, both one-shot operator commands that exit
   after running):

   ```sh
   DAZYFLOW_POSTGRES_DSN="$DSN" \
   DAZYFLOW_MASTER_KEY="$OLD_KEY" \
       dzd --rotate-master-key "$NEW_KEY"
   # logs: "master-key rotation complete: N DEK(s) re-wrapped, …"
   ```

   The command is **re-runnable**: a DEK already on the new key (from
   a prior interrupted run) is detected and skipped, not double-
   wrapped. It fails loudly — leaving the store untouched — if
   `DAZYFLOW_MASTER_KEY` isn't the key that wrapped the existing DEKs.
3. Swap `$DAZYFLOW_MASTER_KEY` to `NEW_KEY` in your `.env` (or whatever
   delivers it) and restart the daemon.
4. Verify reads succeed, then destroy the old key.

> ⚠️ Keep the old key until step 4 verifies. If rotation is interrupted
> partway, re-run step 2 with the same keys before restarting — the
> daemon's running `DAZYFLOW_MASTER_KEY` must match whatever wrapped
> the DEKs on disk.

If the master key was **compromised** (not just being rotated on
schedule), re-wrapping is not enough — also rotate the underlying
credentials (Slack/GitHub/DB tokens) themselves, since their plaintext
may already have leaked.

## Related hardening (see docs/DEPLOY.md for the full list)

All these are env vars set via the same `.env` (see `.env.example`):

- Run behind a TLS-terminating reverse proxy; set
  `DAZYFLOW_TRUST_PROXY_HEADERS=1` so session cookies are `Secure` and
  HSTS is sent.
- `DAZYFLOW_DEV_KEY` defaults off; never set it in production — it
  mints an insecure bearer token at every boot.
- Secrets live only in the per-tenant encrypted store and are
  referenced from flows as `${secret.NAME}`; the daemon's process
  environment is never reachable from a flow. The auth rate limiter
  is fixed at 20/min per IP with a burst of 10 (defense against credential
  stuffing on the auth endpoints) — `ipRateLimiter` in `daemon/ratelimit.go`.
- `DAZYFLOW_HTTP_EGRESS_ALLOW` pins the `http_request` /
  `webhook_send` drops to an allowlist; the IP-level SSRF guard
  (blocks private/loopback/cloud metadata) is always on. Both flow through
  `EgressAllowed` in `drops/net/egress.go`.
- `DAZYFLOW_POSTGRES_DSN` for durable, restart-surviving stores.
