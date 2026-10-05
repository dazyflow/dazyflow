---
title: Build flows with Claude
sidebar_label: Build with Claude
---

# Build flows with Claude

Dazyflow is also an MCP server. Connect Claude to it once and you can ask for
a flow in plain words, from the Claude app on your computer or your phone, and
Claude builds it in your workspace: picks the steps, wires them, sets them up,
and runs it to check. Every change it saves shows up on the canvas, live, in any
browser that has the flow open.

## Connect Claude

You need the address of your Dazyflow server; the connector URL is that
address with `/mcp` on the end, such as `https://flows.example.com/mcp`.
**Connect an assistant** in the account menu shows it, ready to copy.

1. In Claude, open **Settings → Connectors → Add custom connector**.
2. Paste the connector URL and add it.
3. Claude opens Dazyflow. Sign in if you aren't already, check what is asking,
   and choose **Approve**.

That is all. A connector added on claude.ai or in Claude Desktop is there in the
Claude app on your phone too, so you can talk a flow into existence while you
are away from a keyboard.

From a terminal, Claude Code connects the same way and opens the same approval
page:

```
claude mcp add --transport http dazyflow https://flows.example.com/mcp
```

## Watch it build

Open the flow's canvas — Claude gives you the link when it creates the flow —
on your phone or computer, and keep talking. Each change Claude makes lands
there within a second: new steps pop in next to the step they follow, the
steps it touched light up for a few seconds, and a line at the bottom says
what it did and why. If a change lands out of view, the canvas zooms out to
show it.

Ask Claude to undo and it puts the flow back as it was before its last change.
Every change is also in the flow's **History**.

If you edit the canvas yourself while Claude is working, nothing is lost
silently. When Claude saves while you have unsaved changes, the canvas stops
autosaving and asks: **Load their version** or **Keep mine**. And if you save
first, Claude's next change is refused until it has re-read the flow, so it
builds on your edit instead of over it.

## What Claude can do

It acts as you, in the workspace you were in when you approved it, with your
roles: it can do what you can do there, and nothing more. Two things it cannot
do even then, because they are for a person:

- **Decide an approval step** that is set to need a human.
- **Delete a flow**, unless your role includes `graph:admin`. Deleting drops a
  flow's history, and there is no password prompt Claude could answer.

## Disconnecting

**Settings → Connected assistants** lists every connection you approved, with
where it came from and when it was last active. **Disconnect** cuts it off at
once. Connections also end when you change your password or email, are removed
from the organisation, or your account is suspended.

## For operators

The endpoint is `POST /mcp` on the daemon's HTTP port, and the OAuth server
that signs Claude in is built in: discovery under `/.well-known/`, client
registration, `/oauth/authorize` and `/oauth/token`. Set
`DAZYFLOW_PUBLIC_BASE_URL` to the address people reach the server on, since the
OAuth documents name it.

After approval the user is sent back only to `claude.ai`, `claude.com` or a
loopback address (for Claude Code). To allow another client, add its https
host to `DAZYFLOW_MCP_OAUTH_REDIRECT_HOSTS`, comma-separated.

Access tokens last an hour and refresh for up to 30 days of inactivity. Each
refresh issues a new refresh token, and presenting an old one again revokes the
connection, since that only happens when one has leaked.

To use an access key instead of signing in, for `dz-mcp` over stdio or any
client that can send a header, see **Connect an assistant** in the account
menu.
