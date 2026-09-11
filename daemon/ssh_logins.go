// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/dazyflow/dazyflow/core"
)

// A login is who you sign in as: a username and either an SSH key or a
// password. A server is a machine: an address, a port, a host key to expect.
// They were one record, which meant a deploy key shared by ten machines was
// pasted ten times and rotated in ten places — and a form of nine fields that
// mixed the two halves with no seam.
//
//	sshlogin.<name>.username     (required)
//	sshlogin.<name>.password     (secret; one of password/private_key)
//	sshlogin.<name>.private_key  (secret; preferred where the server allows it)
//	sshlogin.<name>.passphrase   (secret; only if the key is encrypted)
//	sshlogin.<name>.public_key   (not secret: the authorized_keys line to hand out)
//
// public_key is kept because it is the thing an operator needs AFTER setting a
// login up — every further server this login reaches needs that line added, and
// a private key cannot be shown again to derive it from.
const (
	secretSSHLoginPrefix = "sshlogin."
	sshLoginFieldUser    = "username"
	sshLoginFieldPass    = "password"
	sshLoginFieldKey     = "private_key"
	sshLoginFieldPhrase  = "passphrase"
	sshLoginFieldPubKey  = "public_key"
)

var sshLoginFields = []string{
	sshLoginFieldUser, sshLoginFieldPass, sshLoginFieldKey,
	sshLoginFieldPhrase, sshLoginFieldPubKey,
}

// SSHLogin is the listing shape: enough to tell one login from another and to
// hand its public half out again. The key, the password and the passphrase
// never come back, only whether they are there.
type SSHLogin struct {
	Name          string `json:"name"`
	Username      string `json:"username,omitempty"`
	PublicKey     string `json:"public_key,omitempty"`
	HasPassword   bool   `json:"has_password"`
	HasSSHKey     bool   `json:"has_ssh_key"`
	HasPassphrase bool   `json:"has_passphrase"`
}

func sshLoginStorageName(name, field string) string {
	return secretSSHLoginPrefix + name + "." + field
}

func validateSSHLoginName(name string) error {
	if !sshCredAccountRe.MatchString(name) {
		return fmt.Errorf("login name must be 1-64 chars of letters, digits, '-' or '_'")
	}
	return nil
}

// parseSSHPrivateKey checks a key before it is stored: one that cannot be read
// is a credential that fails at 03:00 inside a flow rather than here, where
// somebody is looking. Returns the storable form.
func parseSSHPrivateKey(key, passphrase string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", nil
	}
	pem := key + "\n"
	if passphrase != "" {
		if _, err := gossh.ParsePrivateKeyWithPassphrase([]byte(pem), []byte(passphrase)); err != nil {
			return "", fmt.Errorf("private key + passphrase don't parse: %w", err)
		}
		return pem, nil
	}
	if _, err := gossh.ParsePrivateKey([]byte(pem)); err != nil {
		var missing *gossh.PassphraseMissingError
		if errors.As(err, &missing) {
			return "", errors.New("private key is passphrase-protected; provide the passphrase")
		}
		return "", fmt.Errorf("private key doesn't parse: %w", err)
	}
	return pem, nil
}

type sshLoginInput struct {
	Username   string
	Password   string
	PrivateKey string
	Passphrase string
	PublicKey  string
}

func putSSHLogin(ctx context.Context, secrets *EncryptedSecrets, tenant, name string, in sshLoginInput) error {
	if err := validateSSHLoginName(name); err != nil {
		return err
	}
	user := strings.TrimSpace(in.Username)
	if user == "" {
		return errors.New("enter the username to sign in as")
	}
	key, err := parseSSHPrivateKey(in.PrivateKey, in.Passphrase)
	if err != nil {
		return err
	}
	if key == "" && strings.TrimSpace(in.Password) == "" {
		return errors.New("enter either a password or an SSH private key — the server needs one of them to let you in")
	}
	for field, val := range map[string]string{
		sshLoginFieldUser:   user,
		sshLoginFieldPass:   in.Password,
		sshLoginFieldKey:    key,
		sshLoginFieldPhrase: in.Passphrase,
		sshLoginFieldPubKey: strings.TrimSpace(in.PublicKey),
	} {
		if err := putOrClear(ctx, secrets, tenant, sshLoginStorageName(name, field), val); err != nil {
			return err
		}
	}
	return nil
}

func deleteSSHLogin(ctx context.Context, secrets *EncryptedSecrets, tenant, name string) error {
	if err := validateSSHLoginName(name); err != nil {
		return err
	}
	for _, field := range sshLoginFields {
		if err := secrets.Delete(ctx, tenant, sshLoginStorageName(name, field)); err != nil && !errors.Is(err, ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

func listSSHLogins(ctx context.Context, secrets *EncryptedSecrets, tenant string) ([]SSHLogin, error) {
	names, err := secrets.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	byName := map[string]*SSHLogin{}
	for _, n := range names {
		if !strings.HasPrefix(n, secretSSHLoginPrefix) {
			continue
		}
		rest := strings.TrimPrefix(n, secretSSHLoginPrefix)
		dot := strings.LastIndex(rest, ".")
		if dot <= 0 {
			continue
		}
		login, field := rest[:dot], rest[dot+1:]
		l := byName[login]
		if l == nil {
			l = &SSHLogin{Name: login}
			byName[login] = l
		}
		switch field {
		case sshLoginFieldUser:
			l.Username, _ = secrets.GetExact(ctx, tenant, n)
		case sshLoginFieldPubKey:
			l.PublicKey, _ = secrets.GetExact(ctx, tenant, n)
		case sshLoginFieldPass:
			l.HasPassword = true
		case sshLoginFieldKey:
			l.HasSSHKey = true
		case sshLoginFieldPhrase:
			l.HasPassphrase = true
		}
	}
	out := make([]SSHLogin, 0, len(byName))
	for _, l := range byName {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// serversUsingLogin is what stops a delete from breaking flows silently: the
// server rows that name this login, so the refusal can say which.
func serversUsingLogin(ctx context.Context, secrets *EncryptedSecrets, tenant, login string) ([]string, error) {
	creds, err := listSSHCredentials(ctx, secrets, tenant)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range creds {
		if c.Login == login {
			out = append(out, c.Account)
		}
	}
	sort.Strings(out)
	return out, nil
}

type putSSHLoginBody struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	Passphrase string `json:"passphrase"`
	PublicKey  string `json:"public_key"`
}

// listSSHLoginsMe is GET /api/v1/ssh/logins — the org's named logins. Gated on
// secret:read like the servers beside them: the list of accounts a tenant
// automates with is itself worth protecting.
func (h *secretsAPI) listSSHLoginsMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretRead) {
		return
	}
	logins, err := listSSHLogins(r.Context(), h.EncryptedSecrets, p.Tenant)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"logins": logins})
}

func (h *secretsAPI) putSSHLoginMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	name := r.PathValue("name")
	r.Body = http.MaxBytesReader(rw, r.Body, maxSecretValueBytes)
	body, ok := decodeRequestJSON[putSSHLoginBody](rw, r)
	if !ok {
		return
	}
	if err := putSSHLogin(r.Context(), h.EncryptedSecrets, p.Tenant, name, sshLoginInput(body)); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "invalid_login", err.Error())
		return
	}
	h.audit(r.Context(), p, "ssh.login.put", name, "")
	rw.WriteHeader(http.StatusNoContent)
}

func (h *secretsAPI) deleteSSHLoginMe(rw http.ResponseWriter, r *http.Request, p core.Principal) {
	if !h.sshCredsReady(rw, p, core.PermSecretWrite) {
		return
	}
	name := r.PathValue("name")
	used, err := serversUsingLogin(r.Context(), h.EncryptedSecrets, p.Tenant, name)
	if err != nil {
		writeAPIError(rw, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if len(used) > 0 {
		writeAPIError(rw, http.StatusConflict, "login_in_use",
			"still in use by "+strings.Join(used, ", ")+" — point those servers at another login first")
		return
	}
	if err := deleteSSHLogin(r.Context(), h.EncryptedSecrets, p.Tenant, name); err != nil {
		writeAPIError(rw, http.StatusBadRequest, "delete_failed", err.Error())
		return
	}
	h.audit(r.Context(), p, "ssh.login.delete", name, "")
	rw.WriteHeader(http.StatusNoContent)
}
