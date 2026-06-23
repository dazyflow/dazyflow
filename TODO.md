# Web UI/UX — professional polish TODO

Prioritized gaps found by walking the running app. Strong already: node
inspector, Ctrl+K step palette, run detail (timeline/log/retry), a11y labels.

## 1. Flows overview cards (landing page — weakest, first impression) — DONE
- [x] Consistent card layout (equal height via pinned footer; clamped desc)
- [x] Operational signal per card: last run time + outcome dot, recent-runs
      sparkline (RunSparkline). Next-scheduled already shown via schedule chip.
- [x] Search / sort (recent/name/status) / status filter on the flows list
- [~] One-line summary fallback: shows last-run line always; trigger-text
      fallback skipped (needs per-flow graph fetch — not worth N requests)
- [ ] Per-card actions menu (duplicate / rename / delete) — BACKEND-BLOCKED:
      no flow delete/rename/duplicate API exists in client or daemon

## 2. Canvas minimap + auto-layout — DONE
- [x] React Flow MiniMap (themed, pannable, node swatches tinted by run status)
- [x] "Tidy" button — layered left-to-right auto-arrange by dependency depth,
      preserves vertical order, re-fits view

## 3. Runs filtering for debugging at scale — DONE
- [x] Text search (run id / flow name) — client-side over loaded rows + no-match state
- [x] Per-flow filter — server-side via listRuns (paginates correctly)
- [x] Result count ("N+ runs"); Load-more pagination already existed
- [~] Timestamps: kept ABSOLUTE in runs table per user's hybrid decision
      (relative stays on flow cards w/ absolute on hover). Convention honored.
- [ ] Date-range filter — BACKEND-BLOCKED: runs API has no date params;
      client-only over loaded rows would be misleading. Skipped.

## 4. Global command bar / search — DONE
- [x] Global ⌘K command bar (CommandPalette) — jump to any page or flow,
      grouped "Go to" + "Flows", keyboard nav, reuses step-palette styling.
      ⌘K opens it everywhere EXCEPT the editor (step palette keeps ⌘K there),
      per the agreed keybinding split.

## 5. Aggregate home / dashboard — DONE
- [x] /overview Dashboard: 4 stat cards (runs today, success rate, needs
      attention, approvals waiting) + "needs attention" failed-runs list +
      recent activity + flow inventory footer. Nav entry "Overview" (Gauge),
      added to command bar, and made the landing for returning users
      (new users still get /welcome onboarding). All derived client-side.

## 6. Header polish — PARTIAL (scoped to highest-value, well-bounded piece)
- [x] Help button in header + "?" key → keyboard-shortcuts reference modal
      (ShortcutsModal), General + Flow editor sections, platform-aware mod key.
- [~] Notifications / activity — the pending-approvals badge on the Approvals
      nav already covers the main "something needs you" case; a full
      notifications feed is a separate, larger build. Deferred.
- [ ] Breadcrumbs — DEFERRED: the IA is flat (sidebar + page title); breadcrumbs
      add little. Low fit.
- [ ] Undo/redo in editor — DEFERRED: needs a real graph history stack
      (snapshot/coalesce, autosave interplay). Substantial feature, not polish.
      History (version snapshots) already exists for coarse rollback.
