# Proposal: a Hazyflow user manual

**Status:** proposal / RFC · **Scope:** end-user + author documentation ·
**Decision asked:** (1) do we build a manual, (2) what toolchain, (3) what goes in it.

## 1. Why

Today's docs are **operator-facing**: `README.md` (quick start, prod, dev),
`DEPLOY.md`, `SECURITY.md`, `PRIVACY.md`, `COMPLIANCE.md`. There is **nothing
for the people who actually use the product** — the person building a flow in
the web editor. The review in `IMPROVEMENTS.md` (§6) flagged this: no reference
for what each node does, how to compose a flow, or how to troubleshoot a failed
run ("why did my flow pause?").

A manual closes that gap and doubles as: onboarding for new users, a target the
in-app "?" links can point at, and a search surface for "how do I do X".

## 2. Toolchain — and the "Sphinx?" question

**Recommendation: don't use Sphinx. Use [VitePress].** Reasoning:

| Option | Source fmt | Runtime | Fit with this repo |
|--------|-----------|---------|--------------------|
| **VitePress** (recommended) | Markdown | Node/**Vite** | The web app is **already Vite + React + npm**. Zero new language, reuses the toolchain the team runs daily; ships fast static HTML; first-class search. |
| Docusaurus | Markdown/MDX | Node/React | Also reuses the JS stack; heavier than we need, more config. |
| mdBook | Markdown | single Rust binary | Trivial to build, no Node dep — but a *second* ecosystem and weaker theming/search. |
| **Sphinx** | reStructuredText (Markdown via MyST) | **Python** | **Introduces a Python toolchain this project does not otherwise have** (it's pure Go + TS). Powerful (autodoc, cross-refs) but that power targets Python API docs we don't need. rST is a friction tax on contributors already writing Markdown. |

**Verdict on Sphinx:** great tool, wrong project. Its headline features
(Python autodoc, intersphinx) buy us nothing here, and it adds a Python
build/CI dependency plus an unfamiliar markup. Every doc we already have is
Markdown; the build infra is already Vite. VitePress is the low-friction choice
that keeps contributors in one format and one toolchain.

If we ever want the manual served from the app's own domain, VitePress static
output drops straight behind the existing reverse proxy.

[VitePress]: https://vitepress.dev

## 3. Proposed structure

Live under `docs/` (new), built to static HTML, deployed to `/docs`.

```
docs/
  index.md                 Landing: what Hazyflow is, 60-second tour
  getting-started/
    first-flow.md           Build + run your first flow (trigger → action)
    concepts.md             Flows, nodes, ports, edges, runs, drafts vs published
  building-flows/
    triggers.md             cron / webhook / form / poll
    wiring-data.md          ports, ${upstream.…} refs, for_each + ${item.…}
    secrets-and-connections.md   ${secret.…}, OAuth connections, per-list secrets
    publishing.md           draft vs published, why a flow needs publish to fire
  node-reference/          ← see §4 (generated)
    index.md
    <one page per drop / per integration>
  troubleshooting.md        run failed / paused / awaiting approval; redaction marker
  faq.md
```

This mirrors the surfaces that already exist in the product (triggers,
connections, publish state, for_each) so the manual tracks reality.

## 4. Generate the node reference — don't hand-write it

The killer detail: the **node reference can be auto-generated**, not maintained
by hand. Every built-in drop already ships machine-readable metadata the
registry *enforces* at startup — `Manifest.Summary` (one-liner),
`Description`, `Inputs`/`Outputs` ports, `ParamsSchema`, and at least one worked
`Examples` entry (see `engine/registry.go`, and the contract test
`drops/examples_contract_test.go` that holds all ~98 drops to it).

Plan: a small generator reads the catalog (the same data served by the public
`GET /api/v1` catalog endpoints / `engine.Default.Manifests()`) and emits one
Markdown page per drop — title, summary, params table from the schema, ports,
and the worked example verbatim. Wire it as `make docs-gen`. Result: the node
reference is **always correct and never drifts**, because it's derived from the
same manifests the engine runs.

## 5. Rollout (incremental, no big bang)

1. **Scaffold** `docs/` + VitePress, add `make docs` / `make docs-gen`, CI build check.
2. **Author the prose pages** (getting-started, building-flows, troubleshooting) —
   the hand-written core, ~8 pages.
3. **Generate** the node reference from manifests (§4).
4. **Link in-app**: point the editor's help affordances at the relevant page.
5. **Deploy** static output behind the existing proxy at `/docs`; add to CI.

Steps 1–3 are independently shippable and each is a candidate to track as its
own task.

## 6. Open questions

- Host at `/docs` on the app domain, or a separate docs site? (proxy makes `/docs` easy)
- Versioned docs (per release) or always-latest? Latest-only to start.
- Do we want the generator to also publish a JSON catalog for third-party tools?
