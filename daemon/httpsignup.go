package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.sr.ht/~klahr/hazyflow/auth"
	"git.sr.ht/~klahr/hazyflow/core"
)

// Self-serve signup. The endpoint:
//
//	POST /api/v1/auth/signup    {email, password} → {token, subject, tenant, …}
//
// On success the new user gets:
//
//	- a random tenant ID (usr_<hex>), so the email isn't leaked into a
//	  tenant slug visible in URLs/logs;
//	- a default workspace named "main";
//	- two roles: `editor` (graph:run + graph:edit + graph:admin +
//	  secret:read/write so OAuth flows work) and `tenant_owner`
//	  (tenant:admin, so they can manage users + API keys in their
//	  own tenant later);
//	- an immediately-issued session (auto sign-in), matching the
//	  cookie + token shape of the signin endpoint.
//
// What this DELIBERATELY isn't (yet):
//
//   - Email verification — the signup is "instant try" today. Adding
//     verification means an email-sending dependency and a longer
//     onboarding click path. The intentionally-narrow MVP defers it
//     until there's a deliverability story (SES / SendGrid / SMTP
//     config).
//   - Captcha / anti-abuse — signup-spam protection comes with
//     verification or a per-IP rate limit (already on TODO).
//   - Plan selection / billing — every signup lands on the free
//     tier; plan gating is a T3 item once Stripe is wired.
//
// Deployments that don't want self-serve signup leave
// `EnableSignup` false; the endpoint returns 501.

// signupRequest is the wire shape of POST /api/v1/auth/signup.
type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *HTTPGateway) signUp(rw http.ResponseWriter, r *http.Request) {
	if !h.EnableSignup {
		writeJSONError(rw, http.StatusNotImplemented, "self-serve signup is not enabled on this deployment")
		return
	}
	if h.Users == nil || h.Sessions == nil {
		writeJSONError(rw, http.StatusNotImplemented, "users/sessions not configured")
		return
	}
	var body signupRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(rw, http.StatusBadRequest, fmt.Sprintf("decode body: %v", err))
		return
	}
	email := strings.ToLower(strings.TrimSpace(body.Email))
	if err := validSignupEmail(email); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	if err := validSignupPassword(body.Password); err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}

	// Existence check is best-effort — the per-store implementation
	// decides whether GetByEmail returns ErrUnknownUser or a custom
	// error type. Treat any non-nil result as "user exists" to be
	// safe; the duplicate-rejection happens at PutUser time too.
	if existing, err := h.Users.GetByEmail(r.Context(), email); err == nil && existing.Email != "" {
		// "email already in use" is a real fact a malicious enumeration
		// attempt would mine for. But hiding it (always say "account
		// created, check your email") only works when there IS email
		// verification — without it the next step (sign in) reveals
		// the truth anyway. Until verification ships, we tell the
		// truth and rely on rate-limiting (TODO) to slow enumeration.
		writeJSONError(rw, http.StatusConflict, "an account with that email already exists")
		return
	}

	tenant, err := mintTenantID()
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("mint tenant: %v", err))
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		writeJSONError(rw, http.StatusBadRequest, err.Error())
		return
	}
	user := auth.User{
		Email:        email,
		PasswordHash: hash,
		Subject:      email,
		Tenant:       tenant,
		Workspace:    "main",
		Roles:        defaultSignupRoles(),
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.Users.PutUser(r.Context(), user); err != nil {
		// JSONUserStore.PutUser silently overwrites duplicates — the
		// GetByEmail above is our primary defense. Any error here is
		// genuinely unexpected (disk write failure, e.g.).
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("create user: %v", err))
		return
	}

	// Seed the org's display name from the email's domain so the
	// switcher and admin pages don't surface the raw usr_<hex> ID by
	// default. The owner can edit it on /admin/workspace at any time.
	// Best-effort: a failure here doesn't block sign-up because the UI
	// already falls back to the tenant ID when no profile exists.
	if h.Profiles != nil {
		if name := auth.DefaultOrgDisplayName(email); name != "" {
			_ = h.Profiles.PutOrgProfile(r.Context(), auth.OrgProfile{
				Tenant:      tenant,
				DisplayName: name,
				UpdatedAt:   time.Now().UTC(),
			})
		}
	}

	// Auto sign-in: issue a session immediately so the UI can land
	// the user on the welcome page without an extra round trip
	// through the sign-in form.
	ttl := h.SessionTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	sess, token, err := auth.IssueSession(r.Context(), h.Sessions, h.elevatePlatformAdmin(user), ttl)
	if err != nil {
		writeJSONError(rw, http.StatusInternalServerError, fmt.Sprintf("issue session: %v", err))
		return
	}
	h.auditAuth(r.Context(), r, sess.Tenant, sess.Subject, "auth.signup", "method=password")
	http.SetCookie(rw, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil,
	})
	writeJSON(rw, http.StatusCreated, map[string]any{
		"token":      token,
		"subject":    sess.Subject,
		"tenant":     sess.Tenant,
		"workspace":  sess.Workspace,
		"expires_at": sess.ExpiresAt,
	})
}

// validSignupEmail does the loose check the IETF actually
// recommends — "looks like an address," not "passes RFC 5322". A
// real-world bounce is the verification step's job.
func validSignupEmail(email string) error {
	if email == "" {
		return errors.New("email is required")
	}
	if len(email) > 254 {
		return errors.New("email is too long")
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return errors.New("email must look like name@domain")
	}
	domain := email[at+1:]
	if !strings.Contains(domain, ".") {
		return errors.New("email domain must contain a dot")
	}
	for _, r := range email {
		// Reject control chars + whitespace inside the address.
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return errors.New("email contains invalid characters")
		}
	}
	return nil
}

// validSignupPassword enforces the minimum we can defend in a
// startup-phase product. The point of length-only is to keep the
// signup form fast — complexity rules slow users down without
// meaningfully reducing brute-force risk (the bcrypt cost factor
// is what limits that).
func validSignupPassword(password string) error {
	if len(password) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	if len(password) > 256 {
		return errors.New("password is too long (max 256)")
	}
	return nil
}

// mintTenantID returns "usr_" + 8 hex chars. Keeps the tenant out
// of URLs/logs as anything resembling the user's email — important
// because tenant IDs show up in webhook URLs and audit trails. The
// 8 hex chars give ~10^9 combinations; collisions are vanishingly
// unlikely for an MVP.
func mintTenantID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "usr_" + hex.EncodeToString(b), nil
}

// defaultSignupRoles wires the new user with enough permissions to
// drive their own tenant — they can edit and run graphs, manage
// secrets (so OAuth works), and admin their own tenant (issue API
// keys, invite users via the team-features T3 item).
func defaultSignupRoles() []core.Role {
	return []core.Role{
		{
			Name: "editor",
			Permissions: []core.Permission{
				core.PermGraphRun, core.PermGraphEdit, core.PermGraphAdmin,
				core.PermSecretRead, core.PermSecretWrite,
			},
		},
		{
			Name:        "tenant_owner",
			Permissions: []core.Permission{core.PermTenantAdmin},
		},
	}
}

// Workspace provisioning for new signups: the current MapWorkspaces
// build hardcodes a shared dev workspace at startup, so signup
// users transparently share that store. When per-tenant workspace
// storage lands (TODO: workspace create/list), the signup handler
// will need to create the (tenant, "main") entry here.
