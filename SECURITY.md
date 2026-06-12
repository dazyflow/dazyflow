# Security operations

## The master key (`$HAZYFLOW_MASTER_KEY`)

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

Rotating the KEK re-wraps every tenant's DEK under the new key. The
secret ciphertexts (sealed under the per-tenant DEKs) are untouched —
only the wrapped-DEK rows change — so rotation is fast, low-risk, and
requires **no secret re-entry**. `hzd --rotate-master-key` does the
re-wrap; the DEK plaintexts never leave the process.

### Procedure

1. Generate `NEW_KEY` (`openssl rand -base64 32`).
2. Run the re-wrap against the same store the daemon uses. The
   **current** key comes from `$HAZYFLOW_MASTER_KEY`; the new one is
   passed as the `--rotate-master-key` flag (one of only two flags
   `hzd` still accepts, both one-shot operator commands that exit
   after running):

   ```sh
   HAZYFLOW_POSTGRES_DSN="$DSN" \
   HAZYFLOW_MASTER_KEY="$OLD_KEY" \
       hzd --rotate-master-key "$NEW_KEY"
   # logs: "master-key rotation complete: N DEK(s) re-wrapped, …"
   ```

   The command is **re-runnable**: a DEK already on the new key (from
   a prior interrupted run) is detected and skipped, not double-
   wrapped. It fails loudly — leaving the store untouched — if
   `HAZYFLOW_MASTER_KEY` isn't the key that wrapped the existing DEKs.
3. Swap `$HAZYFLOW_MASTER_KEY` to `NEW_KEY` in your `.env` (or whatever
   delivers it) and restart the daemon.
4. Verify reads succeed, then destroy the old key.

> ⚠️ Keep the old key until step 4 verifies. If rotation is interrupted
> partway, re-run step 2 with the same keys before restarting — the
> daemon's running `HAZYFLOW_MASTER_KEY` must match whatever wrapped
> the DEKs on disk.

If the master key was **compromised** (not just being rotated on
schedule), re-wrapping is not enough — also rotate the underlying
credentials (Slack/GitHub/DB tokens) themselves, since their plaintext
may already have leaked.

## Related hardening (see DEPLOY.md for the full list)

All these are env vars set via the same `.env` (see `.env.example`):

- Run behind a TLS-terminating reverse proxy; set
  `HAZYFLOW_TRUST_PROXY_HEADERS=1` so session cookies are `Secure` and
  HSTS is sent.
- `HAZYFLOW_DEV_KEY` defaults off; never set it in production — it
  mints an insecure bearer token at every boot.
- Secrets live only in the per-tenant encrypted store and are
  referenced from flows as `${secret.NAME}`; the daemon's process
  environment is never reachable from a flow. The auth rate limiter
  is fixed at 20/min per IP with a burst of 10 (defense against credential
  stuffing on the auth endpoints).
- `HAZYFLOW_HTTP_EGRESS_ALLOW` pins the `http_request` /
  `webhook_send` drops to an allowlist; the IP-level SSRF guard
  (blocks private/loopback/cloud metadata) is always on.
- `HAZYFLOW_POSTGRES_DSN` for durable, restart-surviving stores.
