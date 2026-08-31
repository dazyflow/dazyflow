<!--
SPDX-FileCopyrightText: 2026 Angels' Ware
SPDX-License-Identifier: AGPL-3.0-or-later
-->

# Dazyflow guide

The hand-written half of the documentation. These pages are read in two places
and both matter: here on the repository host, and in the docs SPA at
[docs.dazyflow.app](https://docs.dazyflow.app), which serves the same files.

Read in order the first time — each page assumes the one above it. This is the
order the docs sidebar uses (`NAV` in `web/src/docs/content.ts`); alphabetical
file listing is not it.

| | Page | What it covers |
|---|---|---|
| 1 | [How Dazyflow works](concepts.md) | Flows, steps, runs, connections — the vocabulary everything else uses. |
| 2 | [Build your first flow](first-flow.md) | End to end, from an empty canvas to a run you can watch. |
| 3 | [Connect an app](connect-an-app.md) | Connections, OAuth and API keys, and where secrets live. |
| 4 | [Triggers & schedules](triggers-and-schedules.md) | Starting a flow on a clock, or on something that happened. |
| 5 | [Forms & webhooks](forms-and-webhooks.md) | Taking input from a person or another system. |
| 6 | [Teams & approvals](teams-and-approvals.md) | Members, roles, and pausing a run for a human decision. |
| 7 | [When a run fails](when-a-flow-fails.md) | Reading a failed run, retries, and failure notifications. |
| 8 | [Runners](runners.md) | Running steps on your own machine, inside your own network. |
| 9 | [MCP servers](mcp-servers.md) | Exposing a flow to an AI assistant, and calling MCP tools from one. |
| 10 | [Web APIs](web-apis.md) | Calling an HTTP API that has no dedicated step. |
| — | [Glossary](glossary.md) | Every term, defined once. Linked from everywhere. |

The **step reference** — one page per step, generated from the drop manifests by
`cmd/docsgen` — has no Markdown source in this repository. Read it at
[docs.dazyflow.app/reference/steps](https://docs.dazyflow.app/reference/steps/),
or build it locally with `make docs-content`.

## Editing these pages

Plain CommonMark plus GitHub tables, rendered by `react-markdown`. Two rules,
both enforced by `web/src/docs/content.test.ts`:

- **Link a sibling page as `./slug.md`.** The renderer strips the extension, so
  one href works in the SPA and on the repository host.
- **Link the step reference as a full `https://docs.dazyflow.app/…` URL** — there
  is no local file to resolve to.

Adding or removing a page means its row in `NAV` and its row in the table above.
A page with one and not the other fails the test.
