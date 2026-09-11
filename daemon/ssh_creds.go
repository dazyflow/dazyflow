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
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

// A named SSH server: the address, the login and the host key to expect, stored
// once per org and picked by a step's `account`. The same model as the Git
// credentials next door, for the same reason — one connection per tenant is
// fine for the one bank drop box an SFTP integration was built around, and
// wrong the moment there are two servers.
//
// SSH and SFTP deliberately share this store rather than keeping one each. They
// are the same server, the same login and the same host key, and the flow worth
// supporting proves it: run pg_dump over SSH, fetch the file it wrote over
// SFTP. Two stores would mean configuring that machine twice and rotating its
// key in two places.
//
// A server is the machine; who signs in to it is an SSHLogin next door, named
// here. The auth fields remain readable because servers saved before the split
// carried their own key, and those keep working untouched — but nothing writes
// them any more.
//
//	sshcred.<account>.host         (required)
//	sshcred.<account>.port         (optional, defaults to 22)
//	sshcred.<account>.login        (which sshlogin.<name> signs in)
//	sshcred.<account>.username     (optional override of the login's username)
//	sshcred.<account>.fingerprint  (host key pin)
//	sshcred.<account>.known_hosts  (host key pin, the OpenSSH way)
//	sshcred.<account>.directory    (SFTP's default folder; ignored by SSH)
//	sshcred.<account>.password     (pre-split: the server's own credential)
//	sshcred.<account>.private_key  (pre-split)
//	sshcred.<account>.passphrase   (pre-split)
const (
	secretSSHCredPrefix = "sshcred."
	sshCredFieldHost    = "host"
	sshCredFieldPort    = "port"
	sshCredFieldLogin   = "login"
	sshCredFieldUser    = "username"
	sshCredFieldPass    = "password"
	sshCredFieldKey     = "private_key"
	sshCredFieldPhrase  = "passphrase"
	sshCredFieldFinger  = "fingerprint"
	sshCredFieldHosts   = "known_hosts"
	sshCredFieldDir     = "directory"
)

// Long enough for a slow handshake on a distant server, short enough that the
// button comes back rather than leaving somebody wondering.
const sshVerifyTimeout = 20 * time.Second

var sshCredFields = []string{
	sshCredFieldHost, sshCredFieldPort, sshCredFieldLogin, sshCredFieldUser,
	sshCredFieldPass, sshCredFieldKey, sshCredFieldPhrase, sshCredFieldFinger,
	sshCredFieldHosts, sshCredFieldDir,
}

// Same slug rule as the git credentials: a dot is the field separator in the
// storage name, so it cannot appear in an account.
var sshCredAccountRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func validateSSHCredAccount(account string) error {
	if !sshCredAccountRe.MatchString(account) {
		return fmt.Errorf("account name must be 1-64 chars of letters, digits, '-' or '_'")
	}
	return nil
}

func sshCredStorageName(account, field string) string {
	return secretSSHCredPrefix + account + "." + field
}

// SSHCredential is the listing shape. The address and login are not secret and
// are what make one account tellable from another in a picker; the key, the
// password and the passphrase are never returned, only reported as present.
type SSHCredential struct {
	Account   string `json:"account"`
	Host      string `json:"host,omitempty"`
	Port      string `json:"port,omitempty"`
	Login     string `json:"login,omitempty"`
	Username  string `json:"username,omitempty"`
	Directory string `json:"directory,omitempty"`
	// Pre-split servers carry their own credential; the page offers to move one.
	HasPassword   bool `json:"has_password"`
	HasSSHKey     bool `json:"has_ssh_key"`
	HasPassphrase bool `json:"has_passphrase"`
	HasHostKey    bool `json:"has_host_key"`
}

func listSSHCredentials(ctx context.Context, secrets *EncryptedSecrets, tenant string) ([]SSHCredential, error) {
	names, err := secrets.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	byAccount := map[string]*SSHCredential{}
	get := func(account string) *SSHCredential {
		c := byAccount[account]
		if c == nil {
			c = &SSHCredential{Account: account}
			byAccount[account] = c
		}
		return c
	}
	plain := func(ctx context.Context, name string) string {
		v, e := secrets.GetExact(ctx, tenant, name)
		if e != nil {
			return ""
		}
		return v
	}
	for _, n := range names {
		if !strings.HasPrefix(n, secretSSHCredPrefix) {
			continue
		}
		rest := strings.TrimPrefix(n, secretSSHCredPrefix)
		dot := strings.LastIndex(rest, ".")
		if dot <= 0 {
			continue
		}
		account, field := rest[:dot], rest[dot+1:]
		c := get(account)
		switch field {
		case sshCredFieldHost:
			c.Host = plain(ctx, n)
		case sshCredFieldPort:
			c.Port = plain(ctx, n)
		case sshCredFieldLogin:
			c.Login = plain(ctx, n)
		case sshCredFieldUser:
			c.Username = plain(ctx, n)
		case sshCredFieldDir:
			c.Directory = plain(ctx, n)
		case sshCredFieldPass:
			c.HasPassword = true
		case sshCredFieldKey:
			c.HasSSHKey = true
		case sshCredFieldPhrase:
			c.HasPassphrase = true
		case sshCredFieldFinger, sshCredFieldHosts:
			c.HasHostKey = true
		}
	}
	out := make([]SSHCredential, 0, len(byAccount))
	for _, c := range byAccount {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Account < out[j].Account })
	return out, nil
}

type sshCredInput struct {
	Host        string
	Port        string
	Login       string
	Username    string
	Password    string
	PrivateKey  string
	Passphrase  string
	Fingerprint string
	KnownHosts  string
	Directory   string
}

func putSSHCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account string, in sshCredInput) error {
	if err := validateSSHCredAccount(account); err != nil {
		return err
	}
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return errors.New("enter the server's address")
	}
	if _, err := sshutil.ParsePort(in.Port); err != nil {
		return err
	}
	login := strings.TrimSpace(in.Login)
	user := strings.TrimSpace(in.Username)
	key, err := parseSSHPrivateKey(in.PrivateKey, in.Passphrase)
	if err != nil {
		return err
	}
	ownCredential := key != "" || strings.TrimSpace(in.Password) != ""
	if login == "" && !ownCredential {
		return errors.New("choose the login this server signs in with — set one up on the Logins page")
	}
	if login != "" {
		if err := validateSSHLoginName(login); err != nil {
			return err
		}
		// A server pointed at a login nobody created fails at run time with
		// nothing to look at; the name is checked while somebody is here.
		l, err := secrets.GetExact(ctx, tenant, sshLoginStorageName(login, sshLoginFieldUser))
		if err != nil || strings.TrimSpace(l) == "" {
			return fmt.Errorf("there is no login called %q — set it up on the Logins page first", login)
		}
	} else if user == "" {
		return errors.New("enter the username to sign in with")
	}
	for field, val := range map[string]string{
		sshCredFieldHost:   host,
		sshCredFieldPort:   strings.TrimSpace(in.Port),
		sshCredFieldLogin:  login,
		sshCredFieldUser:   user,
		sshCredFieldPass:   in.Password,
		sshCredFieldKey:    key,
		sshCredFieldPhrase: in.Passphrase,
		sshCredFieldFinger: strings.TrimSpace(in.Fingerprint),
		sshCredFieldHosts:  strings.TrimSpace(in.KnownHosts),
		sshCredFieldDir:    strings.TrimSpace(in.Directory),
	} {
		if err := putOrClear(ctx, secrets, tenant, sshCredStorageName(account, field), val); err != nil {
			return err
		}
	}
	return nil
}

func deleteSSHCredential(ctx context.Context, secrets *EncryptedSecrets, tenant, account string) error {
	if err := validateSSHCredAccount(account); err != nil {
		return err
	}
	for _, field := range sshCredFields {
		if err := secrets.Delete(ctx, tenant, sshCredStorageName(account, field)); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// LookupSSHCredential resolves an account to a dialable config. This is what the
// SSH and SFTP drops call at run time; the tenant rides on ctx, set by the
// worker before Execute. An account that was never configured comes back as a
// zero Config with no error, so the drop can say "not connected" rather than
// surface a storage error.
func (secrets *EncryptedSecrets) LookupSSHCredential(ctx context.Context, account string) (sshutil.Config, error) {
	var cfg sshutil.Config
	tenant, ok := core.TenantFromContext(ctx)
	if !ok {
		return cfg, errors.New("no tenant in context")
	}
	get := func(field string) (string, error) {
		v, e := secrets.GetExact(ctx, tenant, sshCredStorageName(account, field))
		if e != nil {
			if errors.Is(e, ErrSecretNotFound) {
				return "", nil
			}
			return "", e
		}
		return v, nil
	}
	var port, login string
	fields := map[string]*string{}
	fields[sshCredFieldHost] = &cfg.Host
	fields[sshCredFieldPort] = &port
	fields[sshCredFieldLogin] = &login
	fields[sshCredFieldUser] = &cfg.Username
	fields[sshCredFieldPass] = &cfg.Password
	fields[sshCredFieldKey] = &cfg.PrivateKey
	fields[sshCredFieldPhrase] = &cfg.Passphrase
	fields[sshCredFieldFinger] = &cfg.Fingerprint
	fields[sshCredFieldHosts] = &cfg.KnownHosts
	fields[sshCredFieldDir] = &cfg.Directory
	for _, field := range sshCredFields {
		v, err := get(field)
		if err != nil {
			return sshutil.Config{}, err
		}
		*fields[field] = v
	}
	if cfg.Host == "" {
		return sshutil.Config{}, nil // not configured
	}
	// The two halves meet here. A server's username wins over the login's, so
	// one key can reach a fleet where one machine calls the account something
	// else; everything secret comes from the login and nowhere else.
	if login != "" {
		l := func(field string) (string, error) {
			v, e := secrets.GetExact(ctx, tenant, sshLoginStorageName(login, field))
			if e != nil {
				if errors.Is(e, ErrSecretNotFound) {
					return "", nil
				}
				return "", e
			}
			return v, nil
		}
		user, err := l(sshLoginFieldUser)
		if err != nil {
			return sshutil.Config{}, err
		}
		if user == "" {
			return sshutil.Config{}, fmt.Errorf("this server signs in with the login %q, which no longer exists", login)
		}
		if cfg.Username == "" {
			cfg.Username = user
		}
		for field, dst := range map[string]*string{
			sshLoginFieldPass:   &cfg.Password,
			sshLoginFieldKey:    &cfg.PrivateKey,
			sshLoginFieldPhrase: &cfg.Passphrase,
		} {
			v, err := l(field)
			if err != nil {
				return sshutil.Config{}, err
			}
			*dst = v
		}
	}
	n, err := sshutil.ParsePort(port)
	if err != nil {
		return sshutil.Config{}, err
	}
	cfg.Port = n
	return cfg, nil
}

type putSSHCredBody struct {
	Host        string `json:"host"`
	Port        string `json:"port"`
	Login       string `json:"login"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	PrivateKey  string `json:"private_key"`
	Passphrase  string `json:"passphrase"`
	Fingerprint string `json:"fingerprint"`
	KnownHosts  string `json:"known_hosts"`
	Directory   string `json:"directory"`
}

// listSSHCredsMe is GET /api/v1/ssh/credentials — the org's named servers
// (addresses and logins, never the key or password). Backs both the admin page
// and the account picker on the SSH and SFTP steps. Gated on secret:read, like
// the git credentials: the list of machines a tenant automates is itself worth
// protecting.
func (h *secretsAPI) listSSHCredsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretRead) {
		return
	}
	creds, err := listSSHCredentials(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"credentials": creds})
}

func (h *secretsAPI) putSSHCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	account := r.PathValue("account")
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	body, ok := decodeRequestJSON[putSSHCredBody](rw, r)
	if !ok {
		return
	}
	in := sshCredInput(body)
	if err := putSSHCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account, in); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_credential", err.Error())
		return
	}
	h.audit(r.Context(), p, "ssh.credential.put", account, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *secretsAPI) deleteSSHCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	account := r.PathValue("account")
	if err := deleteSSHCredential(r.Context(), h.EncryptedSecrets, p.Tenant, account); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "ssh.credential.delete", account, "")
	rw.WriteHeader(http.StatusNoContent)
}

// verifySSHCredMe is POST /api/v1/ssh/credentials/{account}/verify.
//
// This is not the convenience the equivalent button is on other integrations.
// Host-key checking here has no default: a credential with neither fingerprint
// nor known_hosts deliberately refuses to connect, and the refusal quotes the
// key the server actually offered. So the intended first run of this is one
// that FAILS, handing the operator the fingerprint to paste in. Without it
// there is no supported way to learn that value from inside the product.
func (h *secretsAPI) verifySSHCredMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// Dialling an arbitrary address on the tenant's behalf is a write-shaped
	// act, not a read: secret:write is what gates configuring the server, and
	// this proves the configuration by using it.
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	account := r.PathValue("account")
	if err := validateSSHCredAccount(account); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_account", err.Error())
		return
	}
	cfg, err := h.EncryptedSecrets.LookupSSHCredential(core.WithTenant(r.Context(), p.Tenant), account)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if cfg.Host == "" {
		writeAPIError(rw, http.StatusNotFound, "not_found", "no such server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sshVerifyTimeout)
	defer cancel()
	if err := sshutil.VerifySSH(ctx, cfg); err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
}

func (h *secretsAPI) sshCredsReady(rw http.ResponseWriter, p core.Principal, perm core.Permission) bool {
	if h.EncryptedSecrets == nil {
		writeAPIError(rw, http.StatusNotImplemented, "not_configured", "encrypted secret store is not configured")
		return false
	}
	if p.Tenant == "" {
		writeAPIError(rw, http.StatusForbidden, "forbidden", "principal has no tenant")
		return false
	}
	if err := core.Require(p, perm); err != nil {
		writeAPIError(rw, http.StatusForbidden, "forbidden", err.Error())
		return false
	}
	return true
}

type scanHostKeyBody struct {
	Host string `json:"host"`
	Port string `json:"port"`
}

// scanSSHHostKeyMe is POST /api/v1/ssh/host-key — what the server offers, for
// an address that need not be saved yet.
//
// The partner of verifySSHCredMe, and the reason the setup guide can ask for a
// host key at the moment it is needed rather than after a deliberate failure.
// It reads a key; it does not trust one. Nothing is stored, and the operator
// still has to accept the fingerprint against what their provider published.
func (h *secretsAPI) scanSSHHostKeyMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	// Dialling an address of the caller's choosing, like verify: write.
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	body, ok := decodeRequestJSON[scanHostKeyBody](rw, r)
	if !ok {
		return
	}
	port, err := sshutil.ParsePort(body.Port)
	if err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_port", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sshVerifyTimeout)
	defer cancel()
	key, err := sshutil.ScanHostKey(ctx, body.Host, port)
	if err != nil {
		writeJSON(rw, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	h.audit(r.Context(), p, "ssh.hostkey.scan", strings.TrimSpace(body.Host), "")
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok":          true,
		"fingerprint": key.Fingerprint,
		"key_type":    key.Type,
		"known_hosts": key.KnownHosts,
	})
}

type generateKeyBody struct {
	Comment string `json:"comment"`
}

// generateSSHKeyMe is POST /api/v1/ssh/keypair — a new ed25519 pair.
//
// The private half comes back so it can be saved with the rest of the form in
// one step, the same way a pasted key is sent; it is never stored by this call.
// The public half is the whole point: it is what the operator adds to the
// server's authorized_keys, and until they do, the credential cannot sign in.
func (h *secretsAPI) generateSSHKeyMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	body, ok := decodeRequestJSON[generateKeyBody](rw, r)
	if !ok {
		return
	}
	comment := strings.TrimSpace(body.Comment)
	if comment == "" {
		comment = "dazyflow"
	}
	priv, pub, err := sshutil.GenerateKeyPair(comment)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r.Context(), p, "ssh.keypair.generate", comment, "")
	writeJSON(rw, http.StatusOK, map[string]any{"private_key": priv, "public_key": pub})
}
