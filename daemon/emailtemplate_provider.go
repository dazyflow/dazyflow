// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dazyflow/dazyflow/auth"
	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/emailtmpl"
)

type EmailTemplateProvider struct {
	Secrets  *EncryptedSecrets
	Profiles auth.OrgProfileStore
}

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

func (p *EmailTemplateProvider) orgLogo(ctx context.Context, tenant string) string {
	if p.Profiles == nil || tenant == "" {
		return ""
	}
	prof, err := p.Profiles.GetOrgProfile(ctx, tenant)
	if err != nil {
		return ""
	}
	return emailtmpl.NormalizeLogo(prof.Icon)
}
