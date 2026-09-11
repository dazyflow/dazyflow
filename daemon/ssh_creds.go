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

	gossh "golang.org/x/crypto/ssh"

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
//	sshcred.<account>.host         (required)
//	sshcred.<account>.port         (optional, defaults to 22)
//	sshcred.<account>.username     (required)
//	sshcred.<account>.password     (secret; one of password/private_key)
//	sshcred.<account>.private_key  (secret; preferred where the server allows it)
//	sshcred.<account>.passphrase   (secret; only if the key is encrypted)
//	sshcred.<account>.fingerprint  (host key pin)
//	sshcred.<account>.known_hosts  (host key pin, the OpenSSH way)
//	sshcred.<account>.directory    (SFTP's default folder; ignored by SSH)
const (
	secretSSHCredPrefix = "sshcred."
	sshCredFieldHost    = "host"
	sshCredFieldPort    = "port"
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
	sshCredFieldHost, sshCredFieldPort, sshCredFieldUser, sshCredFieldPass,
	sshCredFieldKey, sshCredFieldPhrase, sshCredFieldFinger, sshCredFieldHosts,
	sshCredFieldDir,
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
	Account       string `json:"account"`
	Host          string `json:"host,omitempty"`
	Port          string `json:"port,omitempty"`
	Username      string `json:"username,omitempty"`
	Directory     string `json:"directory,omitempty"`
	HasPassword   bool   `json:"has_password"`
	HasSSHKey     bool   `json:"has_ssh_key"`
	HasPassphrase bool   `json:"has_passphrase"`
	HasHostKey    bool   `json:"has_host_key"`
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
	user := strings.TrimSpace(in.Username)
	if user == "" {
		return errors.New("enter the username to sign in with")
	}
	if _, err := sshutil.ParsePort(in.Port); err != nil {
		return err
	}
	key := strings.TrimSpace(in.PrivateKey)
	password := in.Password
	if key == "" && strings.TrimSpace(password) == "" {
		return errors.New("enter either a password or an SSH private key — the server needs one of them to let you in")
	}
	// Parsed before it is stored: a key that cannot be read is a credential that
	// fails at 03:00 in a flow rather than here, where somebody is looking.
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
					return errors.New("private key is passphrase-protected; provide the passphrase")
				}
				return fmt.Errorf("private key doesn't parse: %w", err)
			}
		}
		key = pem
	}
	for field, val := range map[string]string{
		sshCredFieldHost:   host,
		sshCredFieldPort:   strings.TrimSpace(in.Port),
		sshCredFieldUser:   user,
		sshCredFieldPass:   password,
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
	fields := map[string]*string{}
	var port string
	fields[sshCredFieldHost] = &cfg.Host
	fields[sshCredFieldPort] = &port
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
