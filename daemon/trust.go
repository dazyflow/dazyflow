package daemon

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// TrustTier is the signature-derived trust level of an installed artifact. It
// is NEVER read from the artifact's own manifest — a drop can't mark itself
// "official". The tier is computed at install time from which trusted key (if
// any) signed the exact bytes.
type TrustTier string

const (
	TierOfficial  TrustTier = "official"  // signed by Hazy's key
	TierVerified  TrustTier = "verified"  // signed by a reviewed publisher key
	TierCommunity TrustTier = "community" // unsigned, or signed by a key we don't vouch for
)

// Trusted reports whether a tier came from a vouched key (official or verified)
// rather than community (unsigned, or signed by a key we don't vouch for).
func (t TrustTier) Trusted() bool {
	return t == TierOfficial || t == TierVerified
}

// Signature is a detached Ed25519 signature over an artifact's exact bytes (a
// drop's source, or an integration manifest as transported). KeyID names which
// public key in the keyring should verify it.
type Signature struct {
	KeyID string `json:"key_id"`
	Sig   []byte `json:"sig"` // base64 in JSON
}

// TrustedKey is a public key the platform trusts and the tier it confers.
type TrustedKey struct {
	ID        string
	Publisher string
	Tier      TrustTier // TierOfficial or TierVerified
	PublicKey ed25519.PublicKey
}

// Keyring is the set of trusted publisher keys, built from config: Hazy's
// official key plus any verified-publisher keys the operator adds.
type Keyring struct {
	keys map[string]TrustedKey
}

func NewKeyring(keys ...TrustedKey) *Keyring {
	kr := &Keyring{keys: make(map[string]TrustedKey, len(keys))}
	for _, k := range keys {
		kr.keys[k.ID] = k
	}
	return kr
}

// Verify derives the trust tier for content given an optional signature:
//
//   - no signature                     → community (unsigned; allowed, lowest trust)
//   - signed by a key not in the ring  → community (we can't vouch for the signer)
//   - signed by a trusted key, valid   → that key's tier (official / verified)
//   - claims a trusted key but invalid → ERROR (tampered or forged; reject)
//
// The tier is bounded by which of OUR trusted keys actually verifies the exact
// bytes, so a forger can't claim "official" without Hazy's private key — the
// best they can do by signing with their own (un-ringed) key is "community".
func (kr *Keyring) Verify(content []byte, sig *Signature) (TrustTier, string, error) {
	if kr == nil || sig == nil || sig.KeyID == "" {
		return TierCommunity, "", nil
	}
	k, ok := kr.keys[sig.KeyID]
	if !ok {
		return TierCommunity, "", nil
	}
	if len(k.PublicKey) != ed25519.PublicKeySize {
		return "", "", fmt.Errorf("trusted key %q is malformed", sig.KeyID)
	}
	if !ed25519.Verify(k.PublicKey, content, sig.Sig) {
		return "", "", fmt.Errorf("signature for key %q failed verification (tampered or forged)", sig.KeyID)
	}
	return k.Tier, k.Publisher, nil
}

// IDs returns the trusted key ids, sorted — for the boot log.
func (kr *Keyring) IDs() []string {
	out := make([]string, 0, len(kr.keys))
	for id := range kr.keys {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ParseTrustedKey parses one trusted-key spec, "id:tier:publisher:base64key",
// where tier is "official" or "verified" (community is the implicit default for
// unsigned/unknown drops and can't be configured) and base64key is a
// standard-base64 Ed25519 public key. Publisher is a display label and must not
// contain a colon.
func ParseTrustedKey(spec string) (TrustedKey, error) {
	parts := strings.SplitN(spec, ":", 4)
	if len(parts) != 4 {
		return TrustedKey{}, fmt.Errorf("trusted key %q: want id:tier:publisher:base64key", spec)
	}
	id, tierStr, publisher, b64 := parts[0], parts[1], parts[2], parts[3]
	if id == "" {
		return TrustedKey{}, fmt.Errorf("trusted key: id is required")
	}
	var tier TrustTier
	switch tierStr {
	case string(TierOfficial):
		tier = TierOfficial
	case string(TierVerified):
		tier = TierVerified
	default:
		return TrustedKey{}, fmt.Errorf("trusted key %q: tier must be %q or %q", id, TierOfficial, TierVerified)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return TrustedKey{}, fmt.Errorf("trusted key %q: bad base64 public key: %w", id, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return TrustedKey{}, fmt.Errorf("trusted key %q: public key must be %d bytes, got %d", id, ed25519.PublicKeySize, len(raw))
	}
	return TrustedKey{ID: id, Publisher: publisher, Tier: tier, PublicKey: ed25519.PublicKey(raw)}, nil
}

// LoadKeyring builds a Keyring from trusted-key specs (see ParseTrustedKey).
// Blank specs are skipped; a bad spec is collected as an error rather than
// failing the whole load, so one malformed entry doesn't disable trust.
//
// Trusted keys are the root of trust and are intentionally boot config (not
// runtime-mutable via the admin UI) — letting an admin edit them would let them
// mint themselves an "official" tier. The keys are public (no secret), so
// carrying them in deploy config is safe.
func LoadKeyring(specs []string) (*Keyring, []error) {
	var (
		keys []TrustedKey
		errs []error
	)
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		k, err := ParseTrustedKey(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		keys = append(keys, k)
	}
	return NewKeyring(keys...), errs
}
