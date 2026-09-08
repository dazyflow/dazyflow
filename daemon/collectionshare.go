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
	Get(ctx context.Context, tenant, workspace, collection string) (CollectionShare, error)
	List(ctx context.Context, tenant, workspace string) ([]CollectionShare, error)
	Upsert(ctx context.Context, tenant, workspace, collection, token, createdBy string) (CollectionShare, error)
	Delete(ctx context.Context, tenant, workspace, collection string) error
	Lookup(ctx context.Context, token string) (CollectionShare, error)
	DeleteByTenant(ctx context.Context, tenant string) (int, error)
	AnonymizeSubject(ctx context.Context, ident string) (int, error)
}

var errCollectionSharesUnavailable = errors.New("collection sharing is not configured on this deployment")

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
	if _, err := s.BoardRows(ctx, p, tenant, workspace, collection, 1, 0); err != nil {
		return CollectionShare{}, err
	}
	token, err := newShareToken()
	if err != nil {
		return CollectionShare{}, err
	}
	return s.CollectionShares.Upsert(ctx, tenant, workspace, collection, token, p.Subject)
}

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

type PublicCollectionData struct {
	Label       string           `json:"label,omitempty"`
	Icon        string           `json:"icon,omitempty"`
	Collection  string           `json:"collection"`
	GeneratedAt time.Time        `json:"generated_at"`
	Columns     []string         `json:"columns"`
	Rows        []map[string]any `json:"rows"`
	Total       int64            `json:"total"`
	Offset      int              `json:"offset"`
}

func (s *Service) PublicCollection(ctx context.Context, token string, limit, offset int, now time.Time) (PublicCollectionData, error) {
	if s.CollectionShares == nil {
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

func isCollectionSharesUnavailable(err error) bool {
	return errors.Is(err, errCollectionSharesUnavailable)
}

func (c CollectionShare) String() string {
	return fmt.Sprintf("%s/%s/%s", c.Tenant, c.Workspace, c.Collection)
}
