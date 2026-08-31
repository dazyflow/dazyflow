// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
)

// Git credentials are named, per-org auth bundles a git_checkout node selects
// by `account` — the same "configure once, pick per node" model the OAuth
// connectors use. One credential can carry an SSH key (for git@/ssh:// URLs)
// and/or an HTTPS access token / PAT (for https:// URLs), so a single
// "github" credential works for the repo whichever way it's cloned. They live
// in the per-tenant encrypted store under a reserved prefix (shared tenant
// DEK), and never appear on the user Credentials page.
//
//	gitcred.<account>.private_key   (ssh; optional)
//	gitcred.<account>.passphrase    (ssh; optional)
//	gitcred.<account>.known_hosts   (ssh; optional)
//	gitcred.<account>.token         (https PAT; optional)
//	gitcred.<account>.username      (https; optional, defaults to "git")
//
// A credential must carry at least one of {private_key, token}.
const (
	secretGitCredPrefix = "gitcred."
	gitCredFieldKey     = "private_key"
	gitCredFieldPass    = "passphrase"
	gitCredFieldHosts   = "known_hosts"
	gitCredFieldToken   = "token"
	gitCredFieldUser    = "username"
)

// gitCredAccountRe bounds account names to a filesystem/URL-safe slug. A dot
// is disallowed because it's the field separator in the storage name.
var gitCredAccountRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateGitCredAccount(account string) error {
	if !gitCredAccountRe.MatchString(account) {
		return fmt.Errorf("account name must be 1-64 chars of letters, digits, '-' or '_'")
	}
	return nil
}

func gitCredStorageName(account, field string) string {
	return secretGitCredPrefix + account + "." + field
}

// GitCredential is the listing shape: the account name and which parts are
// set. Secret material (key, token) is NEVER returned — the UI shows that a
// credential exists, not its contents. Username is not secret, so it's
// surfaced for display.
type GitCredential struct {
	Account       string `json:"account"`
	HasSSHKey     bool   `json:"has_ssh_key"`
	HasPassphrase bool   `json:"has_passphrase"`
	HasKnownHosts bool   `json:"has_known_hosts"`
	HasToken      bool   `json:"has_token"`
	Username      string `json:"username,omitempty"`
}

// listGitCredentials enumerates the org's git credentials by scanning the
// gitcred. namespace and grouping by account.
func listGitCredentials(ctx context.Context, secrets *EncryptedSecrets, tenant string) ([]GitCredential, error) {
	names, err := secrets.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	byAccount := map[string]*GitCredential{}
	get := func(account string) *GitCredential {
		c := byAccount[account]
		if c == nil {
			c = &GitCredential{Account: account}
			byAccount[account] = c
		}
		return c
	}
	for _, n := range names {
		if !strings.HasPrefix(n, secretGitCredPrefix) {
			continue
		}
		rest := strings.TrimPrefix(n, secretGitCredPrefix)
		dot := strings.LastIndex(rest, ".")
		if dot <= 0 {
			continue
		}
		account, field := rest[:dot], rest[dot+1:]
		c := get(account)
		switch field {
		case gitCredFieldKey:
			c.HasSSHKey = true
		case gitCredFieldPass:
			c.HasPassphrase = true
		case gitCredFieldHosts:
			c.HasKnownHosts = true
		case gitCredFieldToken:
			c.HasToken = true
		case gitCredFieldUser:
			if v, e := secrets.GetExact(ctx, tenant, n); e == nil {
				c.Username = v
			}
		}
	}
	out := make([]GitCredential, 0, len(byAccount))
	for _, c := range byAccount {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, nil
}

// gitCredInput is the writable shape of a credential.
type gitCredInput struct {
	PrivateKey string
	Passphrase string
	KnownHosts string
	Token      string
	Username   string
}

// putGitCredential validates and stores a credential. A private key, if
// given, is parsed up front (with its passphrase) so an unusable key is
// rejected at save time. At least one of {private key, token} is required.
// Optional fields left blank are cleared, so emptying a field unsets it.
func putGitCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account string, in gitCredInput) error {
	if err := validateGitCredAccount(account); err != nil {
		return err
	}
	key := strings.TrimSpace(in.PrivateKey)
	token := strings.TrimSpace(in.Token)
	if key == "" && token == "" {
		return fmt.Errorf("a credential needs an SSH private key, an access token, or both")
	}
	if key != "" {
		pem := key + "\n"
		if in.Passphrase != "" {
			if _, err := gossh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(in.Passphrase)); err != nil {
				return fmt.Errorf("private key + passphrase don't parse: %w", err)
			}
		} else {
			if _, err := gossh.ParsePrivateKey([]byte(pem)); err != nil {
				var missing *gossh.PassphraseMissingError
				if errors.As(err, &missing) {
					return fmt.Errorf("private key is passphrase-protected; provide the passphrase")
				}
				return fmt.Errorf("private key doesn't parse: %w", err)
			}
		}
		if err := secrets.Put(ctx, tenant, gitCredStorageName(account, gitCredFieldKey), pem); err != nil {
			return err
		}
	} else if err := putOrClear(ctx, secrets, tenant, gitCredStorageName(account, gitCredFieldKey), ""); err != nil {
		return err
	}
	// Each remaining field: set when provided, cleared when blank.
	for field, val := range map[string]string{
		gitCredFieldPass:  in.Passphrase,
		gitCredFieldHosts: in.KnownHosts,
		gitCredFieldToken: token,
		gitCredFieldUser:  strings.TrimSpace(in.Username),
	} {
		if err := putOrClear(ctx, secrets, tenant, gitCredStorageName(account, field), val); err != nil {
			return err
		}
	}
	return nil
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

// deleteGitCredential removes all fields of an account. Idempotent.
func deleteGitCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account string) error {
	if err := validateGitCredAccount(account); err != nil {
		return err
	}
	for _, field := range []string{gitCredFieldKey, gitCredFieldPass, gitCredFieldHosts, gitCredFieldToken, gitCredFieldUser} {
		if err := secrets.Delete(ctx, tenant, gitCredStorageName(account, field)); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// ResolvedGitCredential is the full material the git drop needs at clone time.
type ResolvedGitCredential struct {
	PrivateKey string
	Passphrase string
	KnownHosts string
	Token      string
	Username   string
}

// LookupGitCredential resolves an account to its material. This is what the
// git drop's SetGitCredLookup hook calls at clone time; the tenant rides on
// ctx (set by the worker before Execute). A missing account returns a zero
// value (no error) so the drop can surface a clean "not configured" message.
func (secrets *EncryptedSecrets) LookupGitCredential(ctx context.Context, account string) (ResolvedGitCredential, error) {
	var rc ResolvedGitCredential
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return rc, fmt.Errorf("no tenant in context")
	}
	get := func(field string) (string, error) {
		v, e := secrets.GetExact(ctx, tenant, gitCredStorageName(account, field))
		if e != nil {
			if errors.Is(e, ErrSecretNotFound) {
				return "", nil
			}
			return "", e
		}
		return v, nil
	}
	var err error
	if rc.PrivateKey, err = get(gitCredFieldKey); err != nil {
		return rc, err
	}
	if rc.Passphrase, err = get(gitCredFieldPass); err != nil {
		return rc, err
	}
	if rc.KnownHosts, err = get(gitCredFieldHosts); err != nil {
		return rc, err
	}
	if rc.Token, err = get(gitCredFieldToken); err != nil {
		return rc, err
	}
	if rc.Username, err = get(gitCredFieldUser); err != nil {
		return rc, err
	}
	return rc, nil
}

// --- HTTP handlers ----------------------------------------------------

type putGitCredBody struct {
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	KnownHosts string `json:"known_hosts"`
	Token      string `json:"token"`
	Username   string `json:"username"`
}

// listGitCredsMe is GET /api/v1/git/credentials — the org's named git
// credentials (names + which parts are set; never the secret material). Backs
// both the admin page and the git_checkout account picker. Gated on
// secret:read since account names alone reveal which repos a tenant talks to.
func (h *HTTPGateway) listGitCredsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	creds, err := listGitCredentials(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"credentials": creds})
}

// putGitCredMe is PUT /api/v1/git/credentials/{account} — create or replace a
// named git credential. The key (if any) is validated before storage.
func (h *HTTPGateway) putGitCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	body, ok := decodeRequestJSON[putGitCredBody](rw, r)
	if !ok {
		return
	}
	in := gitCredInput{
		PrivateKey: body.PrivateKey,
		Passphrase: body.Passphrase,
		KnownHosts: body.KnownHosts,
		Token:      body.Token,
		Username:   body.Username,
	}
	if err := putGitCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account, in); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_credential", err.Error())
		return
	}
	h.audit(r.Context(), p, "git.credential.put", account, "")
	rw.WriteHeader(http.StatusNoContent)
}

// deleteGitCredMe is DELETE /api/v1/git/credentials/{account}.
func (h *HTTPGateway) deleteGitCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
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
	if err := deleteGitCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "git.credential.delete", account, "")
	rw.WriteHeader(http.StatusNoContent)
}
