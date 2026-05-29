#!/usr/bin/env bash
#
# Publish the example drops as a signed, official-tier marketplace repo.
#
# Produces a standalone git repo (default: ../hazy-official-drops) containing the
# drops + a detached <file>.sig for each, signed by an Ed25519 key generated
# once into .keys/ (gitignored — the PRIVATE key must never be committed). The
# daemon trusts the key via HAZYFLOW_TRUSTED_KEYS (printed at the end); installs
# from this repo then show as `official`.
#
# Re-runnable: the key is generated only if absent; the repo is re-signed and
# re-tagged each run. Override paths with KEYS_DIR / REPO_DIR / TAG env vars.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KEYS_DIR="${KEYS_DIR:-$ROOT/.keys}"
REPO_DIR="${REPO_DIR:-$ROOT/../hazy-official-drops}"
KEY_ID="${KEY_ID:-hazy-official}"
TAG="${TAG:-v1.0.0}"
DROPS=(gmail_send_email slack_send_message)

cd "$ROOT"
HZ=(go run ./cmd/hz-drops)

# 1. Signing key — generated once, reused thereafter.
if [ ! -f "$KEYS_DIR/$KEY_ID.key" ]; then
  "${HZ[@]}" keygen --id "$KEY_ID" --publisher "Hazy Flow" --tier official --out "$KEYS_DIR"
fi

# 2. Copy the drop sources into the repo and sign each.
mkdir -p "$REPO_DIR/drops"
for d in "${DROPS[@]}"; do
  cp "$ROOT/officialdrops/$d.ts" "$REPO_DIR/drops/$d.ts"
done
"${HZ[@]}" sign --key "$KEYS_DIR/$KEY_ID.key" --id "$KEY_ID" "$REPO_DIR/drops/"*.ts

# 3. README for the published repo.
cat > "$REPO_DIR/README.md" <<EOF
# Hazy Flow — Official Drops

Signed marketplace drops, installable from the admin marketplace
(\`/admin/marketplace\` → Install drop) by pointing at this repo.

| Drop | Path | Requires integration |
|------|------|----------------------|
| Gmail send email | \`drops/gmail_send_email.ts\` | \`gmail\` |
| Slack send message | \`drops/slack_send_message.ts\` | \`slack\` |

Each \`.ts\` has a detached \`.ts.sig\` (Ed25519, key id \`$KEY_ID\`). With that key
configured in the daemon's \`HAZYFLOW_TRUSTED_KEYS\`, these install as **official**.
Install the required integration first — the drop install is gated on it.
EOF

# 4. Commit + tag.
cd "$REPO_DIR"
git init -q
git add -A
git -c user.name="Hazy Flow" -c user.email="drops@hazyflow.example" \
  -c commit.gpgSign=false commit -q -m "Official drops $TAG" --allow-empty
# Lightweight tag (override any global config that forces signed/annotated tags),
# matching what the daemon's go-git fetch resolves.
git -c tag.gpgSign=false -c tag.forceSignAnnotated=false tag -f "$TAG" >/dev/null

echo
echo "Published $((${#DROPS[@]})) drop(s) to: $REPO_DIR  (tag $TAG)"
echo "Set on the daemon (HAZYFLOW_TRUSTED_KEYS, ';'-separated):"
echo
sed 's/^/  /' "$KEYS_DIR/$KEY_ID.trustedkey"
