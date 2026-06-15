---
name: complete-hazyflow-tasks
description: Work through every open task in the Hazy "Hazyflow" list and drive each to "completed". Use when asked to "do the Hazyflow tasks", "clear the Hazyflow list", "complete all tasks", or to burn down the backlog tracked in Hazy. Fetches each task (name + description), does the actual engineering work in this repo, then marks it completed via the Hazy REST API, looping until nothing is left pending.
---

# Complete all tasks in the Hazy "Hazyflow" list

The backlog for this project is tracked as tasks in a **Hazy** list named
`Hazyflow`. This skill's goal is simple and terminal: **drive every task in
that list to `completed`**, doing the real work each task describes — not just
flipping a status flag. Keep going until a fresh fetch shows zero `pending`
(and zero `failed`) tasks.

Repo root below is `$ROOT` = `/home/klarre/dev/sourcehut/~klahr/hazyflow`
(the `~klahr` segment is a literal directory name, **not** home expansion —
`~` mid-path is literal in bash, so the path works **unquoted**. Do NOT wrap
it in single quotes; quoting it has been observed to break the redirect).

## API key

The Hazy API uses bearer auth with a personal key (prefix `hzy_u_`). **The key
is a secret and is NOT stored in this file** (this skill is committed to git).
Supply it at run time from one of:

- the untracked file `$ROOT/hazy-key` — read it with `tr -d '\n' < hazy-key`, or
- the environment: `export HAZY_API_KEY=hzy_u_…`.

```bash
KEY="${HAZY_API_KEY:-$(tr -d '\n' < hazy-key 2>/dev/null)}"
[ -n "$KEY" ] || { echo "no Hazy API key: set HAZY_API_KEY or create ./hazy-key" >&2; exit 1; }
```

If neither is present, stop and ask the operator for the key — do not guess.

⚠️ This is a **live personal secret** (full account access). Never commit it,
paste it into logs/PRs, or send it to any host other than `hazy.r8.rs`. Keep
`hazy-key` out of git. If it leaks, revoke it:
`DELETE /api/v1/users/me/api-keys/{id}` (list ids via
`GET /api/v1/users/me/api-keys`) and mint a fresh one in the web app.

## Constants

```
BASE   = https://hazy.r8.rs/api/v1
LIST   = 2990a45b-3f05-4800-8755-a63537af35d1   # the "Hazyflow" list
KEY    = $HAZY_API_KEY (or ./hazy-key) — see "API key" above; never hard-code
```

(If the list id ever changes, rediscover it: `GET /lists` and match
`name == "Hazyflow"`.)

## The loop

Repeat until the list is drained:

1. **Fetch open tasks.** A task is open when `status != "completed"`.
   ```bash
   KEY="${HAZY_API_KEY:-$(tr -d '\n' < hazy-key 2>/dev/null)}"
   curl -s -H "Authorization: Bearer $KEY" \
     "https://hazy.r8.rs/api/v1/lists/2990a45b-3f05-4800-8755-a63537af35d1/tasks" \
     | python3 -m json.tool
   ```
   Each task object carries the fields that matter: `id`, `name`,
   `description` (often the real spec — read it!), and `status`
   (`pending` | `completed` | `failed`).

2. **Pick the next open task** (lowest `position` first is a sensible order).

3. **Read the task, then spawn its subtasks into the same list.** `name` +
   `description` define the deliverable (if it's ambiguous, check comments:
   `GET /tasks/{taskID}/comments`). The parent tasks here are broad
   ("Tests for all built-ins", "Delete flow on mobile") — so the **default**
   action on reading one is to **decompose it into concrete subtasks and add
   those to the same `Hazyflow` list**. Most parent tasks need this; do it
   unless the task is already a single, atomic, do-it-now unit.

   Spawn each subtask with `POST /lists/{listID}/tasks` **into the same list**
   (`LIST` = `2990a45b-3f05-4800-8755-a63537af35d1`, the Hazyflow list — never
   any other list):
   ```bash
   curl -s -X POST \
     -H "Authorization: Bearer $KEY" \
     -H "Content-Type: application/json" \
     -d '{"name":"<subtask title>","description":"<what + acceptance criteria; parent: <name>>","worker_type":"human"}' \
     "https://hazy.r8.rs/api/v1/lists/2990a45b-3f05-4800-8755-a63537af35d1/tasks"
   ```
   `POST /lists/{listID}/tasks` requires `name` and `worker_type` (use
   `"human"` to match the existing tasks); `description`, `due_at`,
   `position`, `icon`, `color` are optional. Spawned subtasks are first-class:
   the loop in step 5 picks them up off the next fetch and drives them to
   `completed` too. Guidance:
   - **Decompose, don't duplicate.** Each subtask must be a concrete,
     independently completable piece — not a vague restatement of the parent.
     Put enough in `description` that a fresh run can do it without re-deriving
     context (what to change + how to know it's done).
   - **Reference the parent** in each subtask's `description` (parent name/id)
     so the relationship is traceable.
   - **Same list, always.** Every spawned task goes into the Hazyflow `LIST`
     above. Never create tasks in another list.
   - **Don't spawn unboundedly.** Spawn the real pieces of the work, not
     busywork. Trivial steps you'll finish in this same pass don't each need
     their own task.

   **Then do the work.** Implement each atomic unit, run the relevant
   tests/build, and verify the change actually works before claiming it done.

   - A **leaf** task (atomic, no useful decomposition) → just do it, then
     mark it `completed` in step 4.
   - A **parent** task you decomposed → complete it (step 4) only once all of
     its spawned subtasks have themselves reached `completed`, with a result
     note pointing at them.

   **Clean up before you call it done.** Once the work passes its tests, do a
   simplification pass over what you touched — leave the code at least as clean
   as you found it:
   - **Remove dead code** the change orphaned: now-unused functions, fields,
     params, imports, branches, i18n keys, and any scaffolding/TODO left
     behind. If you replaced something, delete the old path — don't leave both.
   - **Remove complexity:** collapse needless indirection, fold redundant
     branches, prefer the existing helper/idiom over a new bespoke one, and cut
     comments that no longer match the code.
   - **Re-run the tests/build after cleaning** — simplification must not change
     behaviour. Keep `gofmt`/formatter clean.
   - The repo ships a **`/simplify`** skill (reuse/simplification/efficiency
     pass that applies fixes) and a **`/code-review`** skill — invoke
     `/simplify` on the diff as the cleanup step when a change is non-trivial.
   - Scope the cleanup to code your task touched; don't open unrelated
     refactors (file those as their own subtask in step 3 instead).

4. **Record the outcome on the task, then mark it completed.** Optionally
   stash a short result note in `outputs.result`, then set status:
   ```bash
   curl -s -X PATCH \
     -H "Authorization: Bearer $KEY" \
     -H "Content-Type: application/json" \
     -d '{"status":"completed","outputs":{"result":"<one-line summary of what you did>"}}' \
     "https://hazy.r8.rs/api/v1/tasks/<TASK_ID>"
   ```
   `PATCH /tasks/{taskID}` accepts `status` ∈ {`pending`,`completed`,`failed`},
   plus `description`, `name`, `outputs`, `position`, `due_at`, `preset_id`,
   `worker_config`. Only send the fields you mean to change.

5. **Re-fetch and continue.** Go back to step 1. **Do not stop while any task
   is still `pending`.** Tasks can be added while you work — by others, or by
   you in step 3 — so the exit condition is "a fresh fetch returns no open
   tasks", not "I finished the list I first saw". This is the mechanism that
   makes spawned subtasks get completed: they simply appear on the next fetch.

## Done

When step 1 returns an empty open set, print a final summary: every task name
with its new status (all `completed`), and any result notes you wrote. That is
the goal state.

## Guardrails

- **Never** mark a task `completed` without having actually done it. If you
  genuinely cannot complete one (blocked, needs a human decision, external
  dependency), set it `failed` with an `outputs.result` explaining why, or
  open a question for the operator (`POST /tasks/{taskID}/questions`) and move
  on — do not fake-complete it.
- Keep the API key off the wire to anywhere except `hazy.r8.rs`.
- Don't touch other lists; this skill is scoped to `Hazyflow` only.
