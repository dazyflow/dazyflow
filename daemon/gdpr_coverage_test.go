// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file is the anti-drift guard on the GDPR erasure cascade (Art. 17).
//
// The cascade has been wrong twice in the same way, and both times it was
// invisible: a feature added a tenant-scoped table, nobody thought about
// erasure, and deleteOrgData went on reporting success while the org's rows
// stayed in Postgres. Nothing failed, because a cascade that misses a table
// looks exactly like a cascade that does not need it.
//
// So the disposition is declared here rather than inferred. Every table with a
// tenant column must appear in tenantTableDisposition; a new one fails this
// test until someone writes down what happens to it on erasure. The point is
// not the map — it is that adding a table forces the question to be answered.

// erasureDisposition is what becomes of a tenant-scoped table when its org is
// erased.
type erasureDisposition int

const (
	// erasedByCascade — deleteOrgData removes the tenant's rows.
	erasedByCascade erasureDisposition = iota
	// erasedByIdentity — eraseUserIdentity removes or pseudonymises the rows,
	// keyed on the data subject rather than the tenant.
	erasedByIdentity
	// deliberatelyRetained — the rows outlive the org on purpose. Requires a
	// reason, which is the lawful basis someone had to think about.
	deliberatelyRetained
)

// tenantTableDisposition records, for every tenant-scoped table, what erasure
// does with it. The string is the justification for a retained table and a
// pointer to the erasing step otherwise.
var tenantTableDisposition = map[string]struct {
	how    erasureDisposition
	reason string
}{
	// --- org data, removed by deleteOrgData ---------------------------------
	"jobs":                  {erasedByCascade, "run history + payloads"},
	"run_logs":              {erasedByCascade, "run output, may contain personal data from flows"},
	"bus_events":            {erasedByCascade, "spooled run events"},
	"flow_schedules":        {erasedByCascade, "scheduler enrollment projection"},
	"workspace_shares":      {erasedByCascade, "public overview links"},
	"collection_shares":     {erasedByCascade, "public collection links"},
	"encrypted_secrets":     {erasedByCascade, "connector credentials"},
	"encrypted_secret_deks": {erasedByCascade, "wrapped DEK, dropped with the secrets it opens"},
	"tenant_mcp_servers":    {erasedByCascade, "MCP config + sealed token"},
	"tenant_web_apis":       {erasedByCascade, "web-API catalog config"},
	"tenant_runners":        {erasedByCascade, "runner registrations"},
	"runner_tokens":         {erasedByCascade, "unspent registration tokens"},
	"runner_tasks":          {erasedByCascade, "queued scripts, env and stdin"},
	"git_mirrors":           {erasedByCascade, "remote URL, account, editor email"},
	"drop_switches":         {erasedByCascade, "per-tenant switches only; globals are never touched"},
	"tenant_plans":          {erasedByCascade, "local Stripe mapping; invoices stay in Stripe"},
	"tenant_entitlements":   {erasedByCascade, "tier overrides + admin notes"},
	"usage_counters":        {erasedByCascade, "per-month usage history"},
	"org_auth":              {erasedByCascade, "org SSO config"},
	"org_profiles":          {erasedByCascade, "org display profile"},
	"audit_events":          {erasedByCascade, "hard-deleted for an org; pseudonymised for a user"},
	"support_tickets":       {erasedByCascade, "org support threads"},
	"support_bundles":       {erasedByCascade, "diagnostic bundles"},
	"access_grants":         {erasedByCascade, "support access grants"},
	"memberships":           {erasedByCascade, "also erased per-subject on account deletion"},
	"invitations":           {erasedByCascade, "also erased per-subject on account deletion"},
	"api_keys":              {erasedByCascade, "also erased per-subject on account deletion"},

	// --- subject data, removed by eraseUserIdentity -------------------------
	"users":    {erasedByIdentity, "the data subject's own row"},
	"sessions": {erasedByIdentity, "revoked by subject, and expire on their own"},
}

// indirectlyScoped are tables the column scan cannot see: they carry no tenant
// column and reach one only by joining (run_logs via run_id, bus_events via
// job_id, both landing on jobs.tenant).
//
// They are listed so the disposition map can still document them, and so the
// stale-entry check below does not flag them as gone. The indirection is worth
// knowing about on its own: their erasure has to run BEFORE the jobs rows it
// joins through are deleted, which is why deleteOrgData orders them that way.
var indirectlyScoped = map[string]bool{
	"run_logs":   true,
	"bus_events": true,
}

// tenantTableRe finds a CREATE TABLE body so the column list can be inspected.
var tenantTableRe = regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS\s+([a-z_]+)\s*\((.*?)\n\s*\);`)

// tenantColumnRe matches a literal tenant column declaration.
var tenantColumnRe = regexp.MustCompile(`(?m)^\s*tenant\s+`)

// TestEveryTenantTableHasAnErasureDisposition walks the schema DDL in the
// source tree and fails on any tenant-scoped table nobody has ruled on.
//
// If this test failed because you added a table: decide what erasure does with
// it, wire that into deleteOrgData if it holds org data, and record the answer
// in tenantTableDisposition. Do not add the entry alone to make the test pass —
// the entry is the claim, deleteOrgData is what makes it true.
func TestEveryTenantTableHasAnErasureDisposition(t *testing.T) {
	t.Parallel()
	// Scan the packages that own schema. Relative to daemon/, which is this
	// test's working directory.
	roots := []string{".", "../auth", "../engine", "../core"}

	found := map[string]string{} // table → file that declares it
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				// Sandboxed git checkouts under .hazyflow hold a whole second
				// copy of the tree; scanning it would double every table.
				if name := info.Name(); name == ".hazyflow" || name == ".git" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			name := info.Name()
			if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".sql") {
				return nil
			}
			// Test files build throwaway tables with generated names.
			if strings.HasSuffix(name, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range tenantTableRe.FindAllStringSubmatch(string(src), -1) {
				table, body := m[1], m[2]
				if !tenantColumnRe.MatchString(body) {
					continue
				}
				found[table] = path
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(found) == 0 {
		t.Fatal("no tenant-scoped tables found — the DDL scan is broken, not the schema")
	}

	for table, file := range found {
		d, ok := tenantTableDisposition[table]
		if !ok {
			t.Errorf(`%s (%s) is tenant-scoped but has no erasure disposition.

Deleting an org must not leave its rows behind. Decide which applies:
  - org data      → erase it in deleteOrgData (daemon/gdpr.go), then record
                    erasedByCascade here;
  - subject data  → erase it in eraseUserIdentity, then record erasedByIdentity;
  - kept on purpose → record deliberatelyRetained WITH the lawful basis, and
                    add it to the retention section of docs/PRIVACY.md.`, table, file)
			continue
		}
		if d.how == deliberatelyRetained && strings.TrimSpace(d.reason) == "" {
			t.Errorf("%s is marked deliberatelyRetained with no reason — "+
				"a table that outlives the org needs a stated lawful basis", table)
		}
	}

	// A disposition for a table that no longer exists is stale bookkeeping and
	// makes the map lie about what is covered.
	for table := range tenantTableDisposition {
		if _, ok := found[table]; !ok && !indirectlyScoped[table] {
			t.Errorf("tenantTableDisposition names %q, which no longer exists in the schema", table)
		}
	}
}

// ---- identity columns -------------------------------------------------
//
// The table-level guard above asks "does erasing an ORG take this table's
// rows?". This one asks the question that outlives it: an email column can name
// person A inside a row owned by org B. Erase A's account while B lives on —
// which is every account deletion by a member of a shared org — and the row
// stays, with A's address in it.
//
// That is how the support-ticket residue happened, and it is why coverage is
// declared per column rather than per table.

// columnDisposition is what erasing the PERSON named in a column does to it.
type columnDisposition int

const (
	// erasedWithRow — the row itself only ever survives as long as the subject
	// does, so nothing column-specific is needed.
	erasedWithRow columnDisposition = iota
	// pseudonymisedOnErase — the row outlives the subject and the identifier is
	// replaced with core.ErasedIdentity.
	pseudonymisedOnErase
	// retainedLawfully — the identifier stays, under a stated lawful basis.
	retainedLawfully
	// knownResidual — the identifier CAN outlive the subject and is not yet
	// scrubbed. A tracked gap, not a passing grade; every entry needs a note.
	knownResidual
)

var identityColumnDisposition = map[string]struct {
	how  columnDisposition
	note string
}{
	// The subject's own rows — deleted outright by eraseUserIdentity.
	"users.email":            {erasedWithRow, "the subject's row"},
	"users.subject":          {erasedWithRow, "the subject's row"},
	"sessions.subject":       {erasedWithRow, "revoked by subject"},
	"api_keys.subject":       {erasedWithRow, "deleted by subject"},
	"memberships.user_email": {erasedWithRow, "deleted by email"},
	"invitations.email":      {erasedWithRow, "deleted by email"},
	"platform_admins.email":  {erasedWithRow, "revoked by email"},
	"support_agents.email":   {erasedWithRow, "revoked by email"},

	// Rows that outlive the subject, with the identifier replaced.
	"audit_events.actor":             {pseudonymisedOnErase, "AnonymizeActor"},
	"support_tickets.created_by":     {pseudonymisedOnErase, "AnonymizeSubject"},
	"support_tickets.subject":        {pseudonymisedOnErase, "AnonymizeSubject"},
	"support_tickets.assigned_to":    {pseudonymisedOnErase, "AnonymizeSubject"},
	"support_ticket_messages.author": {pseudonymisedOnErase, "AnonymizeSubject, and the body is blanked"},
	"platform_admins.granted_by":     {pseudonymisedOnErase, "AnonymizeGrantedBy"},
	"support_agents.granted_by":      {pseudonymisedOnErase, "AnonymizeGrantedBy"},
	"blocked_identities.created_by":  {pseudonymisedOnErase, "AnonymizeCreatedBy"},

	// Kept on purpose.
	"blocked_identities.value": {retainedLawfully,
		"the ban itself: a block liftable by asking to be forgotten is not a block. " +
			"Legitimate interest, Art. 17(1)(c) / 6(1)(f) — see docs/PRIVACY.md."},

	// Rows owned by an ORG that name a person who may leave. Deleting the org
	// takes them; these entries cover the other path, where a member of a
	// SHARED org erases their account and the org carries on.
	"git_mirrors.updated_by":        {pseudonymisedOnErase, "AnonymizeSubject"},
	"tenant_mcp_servers.created_by": {pseudonymisedOnErase, "AnonymizeSubject"},
	"tenant_web_apis.created_by":    {pseudonymisedOnErase, "AnonymizeSubject"},
	"tenant_runners.created_by":     {pseudonymisedOnErase, "AnonymizeSubject"},
	"runner_tokens.created_by":      {pseudonymisedOnErase, "AnonymizeSubject, same store as tenant_runners"},
	"workspace_shares.created_by":   {pseudonymisedOnErase, "AnonymizeSubject"},
	"collection_shares.created_by":  {pseudonymisedOnErase, "AnonymizeSubject"},
	"support_bundles.created_by":    {pseudonymisedOnErase, "AnonymizeSubject"},
	"drop_switches.disabled_by":     {pseudonymisedOnErase, "AnonymizeSubject"},
	"memberships.invited_by":        {pseudonymisedOnErase, "AnonymizeSubject"},
	"invitations.invited_by":        {pseudonymisedOnErase, "AnonymizeSubject"},
}

// identityColumnRe matches a column whose value names a person.
var identityColumnRe = regexp.MustCompile(
	`(?m)^\s*(email|actor|author|created_by|granted_by|disabled_by|updated_by|invited_by|assigned_to|user_email|subject|value)\s+`)

// TestEveryIdentityColumnHasADisposition fails on any person-naming column that
// nobody has ruled on.
//
// If this failed because you added a column: decide whether erasing the person
// it names removes the row (erasedWithRow), replaces the value
// (pseudonymisedOnErase), keeps it under a stated basis (retainedLawfully), or
// leaves a residue you are tracking (knownResidual, with a note).
func TestEveryIdentityColumnHasADisposition(t *testing.T) {
	t.Parallel()
	found := map[string]string{} // "table.column" → file
	for _, root := range []string{".", "../auth", "../engine", "../core"} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if n := info.Name(); n == ".hazyflow" || n == ".git" || n == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			n := info.Name()
			if (!strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, ".sql")) || strings.HasSuffix(n, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range tenantTableRe.FindAllStringSubmatch(string(src), -1) {
				table, body := m[1], m[2]
				for _, c := range identityColumnRe.FindAllStringSubmatch(body, -1) {
					found[table+"."+c[1]] = path
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(found) == 0 {
		t.Fatal("no identity columns found — the DDL scan is broken, not the schema")
	}

	var residual []string
	for col, file := range found {
		d, ok := identityColumnDisposition[col]
		if !ok {
			t.Errorf(`%s (%s) names a person but has no disposition.

Erasing that person must not leave their identifier behind. Decide:
  - the row goes with them          → erasedWithRow
  - the row outlives them, value replaced → pseudonymisedOnErase (wire it up first)
  - kept on purpose                 → retainedLawfully, WITH the lawful basis
  - not handled yet                 → knownResidual, WITH a note saying when it leaks`, col, file)
			continue
		}
		if (d.how == retainedLawfully || d.how == knownResidual) && strings.TrimSpace(d.note) == "" {
			t.Errorf("%s is %v with no note — the note is the whole point", col, d.how)
		}
		if d.how == knownResidual {
			residual = append(residual, col)
		}
	}
	for col := range identityColumnDisposition {
		if _, ok := found[col]; !ok {
			t.Errorf("identityColumnDisposition names %q, which no longer exists in the schema", col)
		}
	}

	// Not a failure — these are declared and tracked. Logged so they stay
	// visible in a verbose run instead of decaying into permanent silence.
	if len(residual) > 0 {
		sort.Strings(residual)
		t.Logf("%d identity columns still leak past account erasure (tracked): %s",
			len(residual), strings.Join(residual, ", "))
	}
}
