// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Public collection share links.
//
// A flow that only ever runs by hand still has to put its answer somewhere a
// person can look. The Collections drops give it a place to write (see
// drops/db/builtin_store.go) and results.go lets a signed-in member read it
// back — but the person who wants the answer is often not a member: the
// colleague who asked for the list, the client waiting on the export. Their
// options were a screenshot or an account.
//
// So: one regenerable token per (tenant, workspace, collection), backing a
// login-free read-only table at /board/{token}. The token IS the credential,
// the same model as the workspace-overview link (share.go), the hosted forms
// and the approval links.
//
// The difference from share.go is worth stating plainly, because it decides
// how this may be used. The TV overview publishes a SANITIZED snapshot — flow
// names and run statuses, nothing actionable. This publishes the collection's
// ROWS, whatever they are. There is no field-level redaction and there cannot
// be one: the rows are the reason for the link. A share here is therefore a
// deliberate act of publication, gated on graph:edit (a read-only viewer
// cannot publish one), audited, and revocable — and the UI says so before the
// link is minted.
//
// No new storage for the data itself: the read path is results.go's BoardRows
// against the same workspace store, so the public page and the Collections
// page cannot disagree, and the GDPR erasure cascade already covers the rows.
// Only the token needs a table.

package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// CollectionShare is one public collection link.
type CollectionShare struct {
	Tenant     string    `json:"-"`
	Workspace  string    `json:"-"`
	Collection string    `json:"collection"`
	Token      string    `json:"token"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
}

// CollectionShareStore persists public collection links. One row per
// (tenant, workspace, collection); Upsert rotates the token in place so a
// collection always has at most one live link.
type CollectionShareStore interface {
	// Get returns the collection's current share, or core.ErrNotFound when
	// none has been created.
	Get(ctx context.Context, tenant, workspace, collection string) (CollectionShare, error)
	// List returns every share in the workspace, ordered by collection. Backs
	// the Collections page's "this one is public" marking, which is the only
	// way a member sees that a link exists without opening each dialog.
	List(ctx context.Context, tenant, workspace string) ([]CollectionShare, error)
	// Upsert creates or rotates the collection's share to the given token.
	Upsert(ctx context.Context, tenant, workspace, collection, token, createdBy string) (CollectionShare, error)
	// Delete revokes the collection's share. Idempotent.
	Delete(ctx context.Context, tenant, workspace, collection string) error
	// Lookup resolves a token back to its share (the public path). Returns
	// core.ErrNotFound for an unknown/rotated token.
	Lookup(ctx context.Context, token string) (CollectionShare, error)
	// DeleteByTenant erases every share for a tenant — the GDPR/org-erasure
	// cascade hook (see gdpr.go's tenantEraser).
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	// AnonymizeSubject pseudonymises an erased person's identifier wherever it
	// appears. Same treatment as share.go: the rows belong to an org and
	// outlive the person who minted the link.
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
}

// errCollectionSharesUnavailable is the no-store-wired case, reported as
// not-configured rather than as a server fault.
var errCollectionSharesUnavailable = errors.New("collection sharing is not configured on this deployment")

// GetCollectionShare returns a collection's current link, or ok=false when it
// has none. Read access is gated on workspace membership.
func (s *Service) GetCollectionShare(ctx context.Context, p core.Principal, tenant, workspace, collection string) (CollectionShare, bool, error) {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return CollectionShare{}, false, err
	}
	if err := validateBoardName(collection); err != nil {
		return CollectionShare{}, false, err
	}
	if s.CollectionShares == nil {
		return CollectionShare{}, false, errCollectionSharesUnavailable
	}
	sh, err := s.CollectionShares.Get(ctx, tenant, workspace, collection)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return CollectionShare{}, false, nil
		}
		return CollectionShare{}, false, err
	}
	return sh, true, nil
}

// ListCollectionShares returns every live link in the workspace. Membership is
// enough — knowing WHICH collections are published is exactly what a member
// needs in order to notice one that shouldn't be.
func (s *Service) ListCollectionShares(ctx context.Context, p core.Principal, tenant, workspace string) ([]CollectionShare, error) {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return nil, err
	}
	if s.CollectionShares == nil {
		// An unconfigured store means no link can exist, which is a true and
		// useful answer for a listing — an empty list, not an error the
		// Collections page would have to render.
		return []CollectionShare{}, nil
	}
	out, err := s.CollectionShares.List(ctx, tenant, workspace)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []CollectionShare{}
	}
	return out, nil
}

// CreateCollectionShare mints (or rotates) a collection's public link.
// Rotating invalidates any link handed out earlier.
//
// graph:edit, not the read-level graph:run that reading a collection takes:
// this publishes the rows to anyone holding the URL, and a viewer who may
// only look at data must not be able to publish it.
func (s *Service) CreateCollectionShare(ctx context.Context, p core.Principal, tenant, workspace, collection string) (CollectionShare, error) {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return CollectionShare{}, err
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		return CollectionShare{}, err
	}
	if err := validateBoardName(collection); err != nil {
		return CollectionShare{}, err
	}
	if s.CollectionShares == nil {
		return CollectionShare{}, errCollectionSharesUnavailable
	}
	// Refuse to mint a link for a collection that isn't there. Without this a
	// typo yields a live URL that 404s for whoever it was sent to, and the
	// sender has no way to tell that from a revoked one.
	if _, err := s.BoardRows(ctx, p, tenant, workspace, collection, 1, 0); err != nil {
		return CollectionShare{}, err
	}
	token, err := newShareToken()
	if err != nil {
		return CollectionShare{}, err
	}
	return s.CollectionShares.Upsert(ctx, tenant, workspace, collection, token, p.Subject)
}

// DeleteCollectionShare revokes a collection's link. Idempotent. Same
// edit-level gate as creating one.
func (s *Service) DeleteCollectionShare(ctx context.Context, p core.Principal, tenant, workspace, collection string) error {
	if err := core.RequireWorkspace(p, tenant, workspace); err != nil {
		return err
	}
	if err := core.Require(p, core.PermGraphEdit); err != nil {
		return err
	}
	if err := validateBoardName(collection); err != nil {
		return err
	}
	if s.CollectionShares == nil {
		return errCollectionSharesUnavailable
	}
	return s.CollectionShares.Delete(ctx, tenant, workspace, collection)
}

// PublicCollectionData is what the public table page renders.
type PublicCollectionData struct {
	// Label and Icon are the org's display name and logo, so the page is
	// recognisably somebody's rather than anonymous. Both already public on
	// the sign-in page. Empty when the org has set neither.
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
	// Collection is the collection's own name — the page's title.
	Collection  string           `json:"collection"`
	GeneratedAt time.Time        `json:"generated_at"`
	Columns     []string         `json:"columns"`
	Rows        []map[string]any `json:"rows"`
	Total       int64            `json:"total"`
	// Offset is the window this payload starts at, echoed back so the page can
	// label its rows and page without tracking the request it made.
	Offset int `json:"offset"`
}

// PublicCollection resolves a share token and returns a window of the
// collection behind it. No principal: the token is the authorization, so this
// reads the workspace store directly (the same pattern the webhook-trigger and
// public-overview paths use). Returns core.ErrNotFound for an unknown token or
// a collection that has since been cleared.
func (s *Service) PublicCollection(ctx context.Context, token string, limit, offset int, now time.Time) (PublicCollectionData, error) {
	if s.CollectionShares == nil {
		// No store on this deployment → no link can exist. An unknown link
		// (404), not a server error.
		return PublicCollectionData{}, core.ErrNotFound
	}
	share, err := s.CollectionShares.Lookup(ctx, token)
	if err != nil {
		return PublicCollectionData{}, err // core.ErrNotFound bubbles to a 404
	}

	// The empty principal is deliberate: BoardRows does no authz of its own
	// (results.go says so — the HTTP layer is the barrier on that surface),
	// and here the barrier is the token, already checked above. Passing a
	// fabricated principal would only make it look like a check happened.
	page, err := s.BoardRows(ctx, core.Principal{}, share.Tenant, share.Workspace, share.Collection, limit, offset)
	if err != nil {
		if errors.Is(err, errBoardNotFound) {
			// The collection was cleared after the link was minted. Report it
			// as an unknown link: the reader can't act on the distinction, and
			// it keeps the public surface from confirming which workspaces
			// hold which collection names.
			return PublicCollectionData{}, core.ErrNotFound
		}
		return PublicCollectionData{}, err
	}

	label, icon := s.workspaceBrand(ctx, share.Tenant)
	rows := make([]map[string]any, 0, len(page.Rows))
	for _, r := range page.Rows {
		// Drop the row-delete handle. It is a private key for an authenticated
		// mutation and has no business on a read-only public page, columns or
		// not.
		out := make(map[string]any, len(r))
		for k, v := range r {
			if k == boardRowIDKey {
				continue
			}
			out[k] = v
		}
		rows = append(rows, out)
	}
	cols := page.Columns
	if cols == nil {
		cols = []string{}
	}
	return PublicCollectionData{
		Label:       label,
		Icon:        icon,
		Collection:  share.Collection,
		GeneratedAt: now,
		Columns:     cols,
		Rows:        rows,
		Total:       page.Total,
		Offset:      offset,
	}, nil
}

// collectionShareError is the shared service-error mapping helper, kept next
// to the sentinel it knows about.
func isCollectionSharesUnavailable(err error) bool {
	return errors.Is(err, errCollectionSharesUnavailable)
}

// String is here so a share can be logged/audited by identity without the
// token leaking into a log line.
func (c CollectionShare) String() string {
	return fmt.Sprintf("%s/%s/%s", c.Tenant, c.Workspace, c.Collection)
}
