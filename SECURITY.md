# Security operations

## The master key (`--master-key` / `$HAZYFLOW_MASTER_KEY`)

hzd's built-in secret store uses **envelope encryption**:

- **KEK** (Key-Encryption-Key) = the master key. A 32-byte AES-256 key
  you provide. Held only in process memory; hzd never writes it to disk.
- **DEK** (Data-Encryption-Key) = one per tenant, generated on that
  tenant's first secret write, then **wrapped (encrypted) by the KEK**
  and stored. Each secret is sealed with AES-256-GCM under its tenant's
  DEK.

So the master key protects every tenant's DEK, which protects every
secret. It is the single most sensitive piece of configuration.

### Generating it

```sh
openssl rand -base64 32        # 32 bytes, base64-encoded — what hzd expects
```

hzd validates it decodes to exactly 32 bytes and exits otherwise.

### Storing it

- **Never** commit it, bake it into an image, or put it in the graph
  store. Inject it at runtime via `$HAZYFLOW_MASTER_KEY`.
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

> ⚠️ **Automated re-wrap tooling is not built yet** (`v2` in the code —
> see `daemon/encrypted_secrets.go`). Rotating the KEK means re-wrapping
> every tenant's DEK under the new key; until that command ships, use the
> interim procedure below. This is a known gap tracked in `TODO.md`.

### Target procedure (once re-wrap tooling lands)

1. Generate `NEW_KEY` (`openssl rand -base64 32`).
2. Run the re-wrap job with both keys: for each tenant, unwrap the DEK
   with the OLD KEK and re-wrap it with the NEW KEK in a transaction.
   Secrets (sealed under the DEK) are untouched — only the wrapped-DEK
   rows change, so this is fast and low-risk.
3. Swap `$HAZYFLOW_MASTER_KEY` to `NEW_KEY` and restart.
4. Destroy the old key after verifying reads succeed.

### Interim procedure (today, no tooling)

Because DEKs can't yet be re-wrapped in place:

1. Stand up the new key on a fresh secret namespace (new DB, or new
   tenant DEK rows after a wipe).
2. Re-enter each tenant's secrets via `PUT /api/v1/secrets/{name}` (or
   re-run the OAuth connect flows) so they're sealed under DEKs wrapped
   by the new key.
3. Cut over `$HAZYFLOW_MASTER_KEY` and restart.

For a single-tenant / low-secret-count dev or pilot deployment this is a
few minutes of work. For multi-tenant production, prioritise building the
re-wrap command before you need to rotate under pressure.

## Related hardening (see DEPLOY.md for the full list)

- Run behind a TLS-terminating reverse proxy; start hzd with
  `--trust-proxy-headers` so session cookies are `Secure` + HSTS is sent.
- `--dev-key` defaults off; never enable it in production.
- `--auth-rate-per-min` throttles credential stuffing on the auth
  endpoints.
- `--http-egress-allow` pins the `http_request` / `webhook_send` drops to
  an allowlist; the IP-level SSRF guard (blocks private/loopback/cloud
  metadata) is always on.
- `--postgres-dsn` for durable, restart-surviving stores.
