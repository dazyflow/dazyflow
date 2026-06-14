package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"git.sr.ht/~klahr/hazyflow/core"
)

// Git SSH credentials are named, per-org key bundles a git_checkout node
// selects by `account` — the same "configure once, pick per node" model the
// OAuth connectors use, but for raw SSH key material (which can't come from
// an OAuth redirect). They live in the per-tenant encrypted store under a
// reserved prefix, so they share the tenant DEK and never appear on the
// user Credentials page.
//
//	gitssh.<account>.private_key   (required)
//	gitssh.<account>.passphrase    (optional)
//	gitssh.<account>.known_hosts   (optional)
const (
	secretGitSSHPrefix = "gitssh."
	gitSSHFieldKey     = "private_key"
	gitSSHFieldPass    = "passphrase"
	gitSSHFieldHosts   = "known_hosts"
)

// gitSSHAccountRe bounds account names to a filesystem/URL-safe slug. A dot
// is disallowed because it's the field separator in the storage name.
var gitSSHAccountRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateGitSSHAccount(account string) error {
	if !gitSSHAccountRe.MatchString(account) {
		return fmt.Errorf("account name must be 1-64 chars of letters, digits, '-' or '_'")
	}
	return nil
}

func gitSSHStorageName(account, field string) string {
	return secretGitSSHPrefix + account + "." + field
}

// GitSSHCredential is the listing shape: the account name and which fields
// are set. Key material is NEVER returned by the API — the UI shows that a
// credential exists, not its contents (same contract as the secrets list).
type GitSSHCredential struct {
	Account       string `json:"account"`
	HasPassphrase bool   `json:"has_passphrase"`
	HasKnownHosts bool   `json:"has_known_hosts"`
}

// listGitSSHCredentials enumerates the org's SSH credentials by scanning the
// gitssh. namespace and grouping by account.
func listGitSSHCredentials(ctx context.Context, secrets *EncryptedSecrets, tenant string) ([]GitSSHCredential, error) {
	names, err := secrets.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	byAccount := map[string]*GitSSHCredential{}
	for _, n := range names {
		if !strings.HasPrefix(n, secretGitSSHPrefix) {
			continue
		}
		rest := strings.TrimPrefix(n, secretGitSSHPrefix)
		dot := strings.LastIndex(rest, ".")
		if dot <= 0 {
			continue
		}
		account, field := rest[:dot], rest[dot+1:]
		c := byAccount[account]
		if c == nil {
			c = &GitSSHCredential{Account: account}
			byAccount[account] = c
		}
		switch field {
		case gitSSHFieldPass:
			c.HasPassphrase = true
		case gitSSHFieldHosts:
			c.HasKnownHosts = true
		}
	}
	out := make([]GitSSHCredential, 0, len(byAccount))
	for _, c := range byAccount {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, nil
}

// putGitSSHCredential validates and stores a credential. The private key is
// parsed up front (with the passphrase, if any) so an unusable key is
// rejected at save time rather than at clone time. Optional fields left
// empty are deleted, so clearing a passphrase actually unsets it.
func putGitSSHCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account, privateKey, passphrase, knownHosts string) error {
	if err := validateGitSSHAccount(account); err != nil {
		return err
	}
	privateKey = strings.TrimSpace(privateKey) + "\n"
	if strings.TrimSpace(privateKey) == "" {
		return fmt.Errorf("private_key is required")
	}
	if passphrase != "" {
		if _, err := gossh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase)); err != nil {
			return fmt.Errorf("private key + passphrase don't parse: %w", err)
		}
	} else {
		if _, err := gossh.ParsePrivateKey([]byte(privateKey)); err != nil {
			var missing *gossh.PassphraseMissingError
			if errors.As(err, &missing) {
				return fmt.Errorf("private key is passphrase-protected; provide the passphrase")
			}
			return fmt.Errorf("private key doesn't parse: %w", err)
		}
	}
	if err := secrets.Put(ctx, tenant, gitSSHStorageName(account, gitSSHFieldKey), privateKey); err != nil {
		return err
	}
	// Passphrase / known_hosts: set when provided, cleared when blank.
	if err := putOrClear(ctx, secrets, tenant, gitSSHStorageName(account, gitSSHFieldPass), passphrase); err != nil {
		return err
	}
	return putOrClear(ctx, secrets, tenant, gitSSHStorageName(account, gitSSHFieldHosts), knownHosts)
}

func putOrClear(ctx context.Context, secrets *EncryptedSecrets, tenant, name, value string) error {
	if strings.TrimSpace(value) == "" {
		err := secrets.Delete(ctx, tenant, name)
		if err != nil && errors.Is(err, ErrSecretNotFound) {
			return nil
		}
		return err
	}
	return secrets.Put(ctx, tenant, name, value)
}

// deleteGitSSHCredential removes all fields of an account. Idempotent.
func deleteGitSSHCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account string) error {
	if err := validateGitSSHAccount(account); err != nil {
		return err
	}
	for _, field := range []string{gitSSHFieldKey, gitSSHFieldPass, gitSSHFieldHosts} {
		if err := secrets.Delete(ctx, tenant, gitSSHStorageName(account, field)); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// LookupGitSSHCredential resolves an account to its key material. This is the
// function the git drop's SetSSHCredLookup hook calls at clone time; the
// tenant rides on ctx (set by the worker before Execute). A missing account
// returns empty strings (no error) so the drop can surface a clean "not
// configured" message.
func (secrets *EncryptedSecrets) LookupGitSSHCredential(ctx context.Context, account string) (privateKey, passphrase, knownHosts string, err error) {
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return "", "", "", fmt.Errorf("no tenant in context")
	}
	get := func(field string) (string, error) {
		v, e := secrets.GetExact(ctx, tenant, gitSSHStorageName(account, field))
		if e != nil {
			if errors.Is(e, ErrSecretNotFound) {
				return "", nil
			}
			return "", e
		}
		return v, nil
	}
	if privateKey, err = get(gitSSHFieldKey); err != nil || privateKey == "" {
		return "", "", "", err
	}
	if passphrase, err = get(gitSSHFieldPass); err != nil {
		return "", "", "", err
	}
	if knownHosts, err = get(gitSSHFieldHosts); err != nil {
		return "", "", "", err
	}
	return privateKey, passphrase, knownHosts, nil
}

// --- HTTP handlers ----------------------------------------------------

type putGitSSHBody struct {
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	KnownHosts string `json:"known_hosts"`
}

// listGitSSHCredsMe is GET /api/v1/git/ssh-credentials — the org's named SSH
// credentials (names + which fields are set; never the key). Backs both the
// admin page and the git_checkout account picker. Gated on secret:read since
// account names alone reveal which repos a tenant talks to.
func (h *HTTPGateway) listGitSSHCredsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretRead); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	creds, err := listGitSSHCredentials(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"credentials": creds})
}

// putGitSSHCredMe is PUT /api/v1/git/ssh-credentials/{account} — create or
// replace a named SSH credential. The key is validated before storage.
func (h *HTTPGateway) putGitSSHCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	account := r.PathValue("account")
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	var body putGitSSHBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "decode_failed", "decode body: "+err.Error())
		return
	}
	if err := putGitSSHCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account, body.PrivateKey, body.Passphrase, body.KnownHosts); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_credential", err.Error())
		return
	}
	h.audit(r.Context(), p, "git.ssh_credential.put", account, "")
	rw.WriteHeader(http.StatusNoContent)
}

// deleteGitSSHCredMe is DELETE /api/v1/git/ssh-credentials/{account}.
func (h *HTTPGateway) deleteGitSSHCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return
	}
	if err := core.Require(p, core.PermSecretWrite); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return
	}
	account := r.PathValue("account")
	if err := deleteGitSSHCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "git.ssh_credential.delete", account, "")
	rw.WriteHeader(http.StatusNoContent)
}
