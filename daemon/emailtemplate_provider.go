package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"git.sr.ht/~klahr/dazyflow/auth"
	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/internal/emailtmpl"
)

// EmailTemplateProvider resolves an email-template ID to its layout shell HTML
// at run time, implementing engine.EmailTemplateProvider. Built-in templates
// (ID prefixed "builtin:") come from the global catalog; everything else is an
// org template read from the encrypted store under the "emailtmpl." namespace,
// scoped to the job's tenant. It also surfaces the org's logo (OrgProfile.Icon)
// so shells with a {{.Logo}} slot render the tenant's mark.
type EmailTemplateProvider struct {
	Secrets *EncryptedSecrets
	// Profiles is optional; when nil (or the org has no profile/icon) the
	// resolved logo is empty and shells fall back to no logo.
	Profiles auth.OrgProfileStore
}

// TemplateHTML returns the shell HTML for id scoped to tenant, plus the org
// logo. ok is false for an unknown id (built-in miss or no stored org
// template); err is reserved for store/decrypt failures.
func (p *EmailTemplateProvider) TemplateHTML(ctx context.Context, tenant, id string) (html, logo string, ok bool, err error) {
	logo = p.orgLogo(ctx, tenant)

	if emailtmpl.IsBuiltinID(id) {
		t, found := emailtmpl.Builtin(id)
		if !found {
			return "", logo, false, nil
		}
		return t.HTML, logo, true, nil
	}

	if p.Secrets == nil || tenant == "" {
		return "", logo, false, nil
	}
	raw, err := p.Secrets.GetExact(ctx, tenant, secretEmailTmplPrefix+id)
	if err != nil {
		// A missing template is a clean miss (ok=false); only a real
		// store/decrypt error propagates.
		if errors.Is(err, ErrSecretNotFound) {
			return "", logo, false, nil
		}
		return "", logo, false, err
	}
	var t core.EmailTemplate
	if err := json.Unmarshal([]byte(raw), &t); err != nil {
		return "", logo, false, err
	}
	return t.HTML, logo, true, nil
}

// orgLogo returns the tenant's logo (OrgProfile.Icon) for shells that show it,
// or "" when unavailable. A missing profile is non-fatal.
func (p *EmailTemplateProvider) orgLogo(ctx context.Context, tenant string) string {
	if p.Profiles == nil || tenant == "" {
		return ""
	}
	prof, err := p.Profiles.GetOrgProfile(ctx, tenant)
	if err != nil {
		return ""
	}
	return prof.Icon
}
