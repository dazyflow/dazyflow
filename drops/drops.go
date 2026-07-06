// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package drops is an umbrella import: depending on this package pulls in
// every built-in drop via side-effect init() registration. Every drop is
// native Go (compiled into the binary) — including the connectors
// (slack, github, gmail, sheets, notion, claude, excel, ntfy, webhooks).
// Each subdirectory is one bucket; the catalog UI groups them at runtime by
// the Manifest.Integration / Manifest.Category fields, independent of this
// directory layout. Connectors that need credentials use the OAuth provider
// registry (a separate concept) or a ${secret.…} token.
package drops

import (
	_ "git.sr.ht/~klahr/dazyflow/drops/claude"
	_ "git.sr.ht/~klahr/dazyflow/drops/datetime"
	_ "git.sr.ht/~klahr/dazyflow/drops/db"
	_ "git.sr.ht/~klahr/dazyflow/drops/discord"
	_ "git.sr.ht/~klahr/dazyflow/drops/drive"
	_ "git.sr.ht/~klahr/dazyflow/drops/elks"
	_ "git.sr.ht/~klahr/dazyflow/drops/encoding"
	_ "git.sr.ht/~klahr/dazyflow/drops/excel"
	_ "git.sr.ht/~klahr/dazyflow/drops/flow"
	_ "git.sr.ht/~klahr/dazyflow/drops/fortnox"
	_ "git.sr.ht/~klahr/dazyflow/drops/gcal"
	_ "git.sr.ht/~klahr/dazyflow/drops/geo"
	_ "git.sr.ht/~klahr/dazyflow/drops/git"
	_ "git.sr.ht/~klahr/dazyflow/drops/github"
	_ "git.sr.ht/~klahr/dazyflow/drops/gmail"
	_ "git.sr.ht/~klahr/dazyflow/drops/homeassistant"
	_ "git.sr.ht/~klahr/dazyflow/drops/io"
	_ "git.sr.ht/~klahr/dazyflow/drops/klarna"
	_ "git.sr.ht/~klahr/dazyflow/drops/mqtt"
	_ "git.sr.ht/~klahr/dazyflow/drops/net"
	_ "git.sr.ht/~klahr/dazyflow/drops/notify"
	_ "git.sr.ht/~klahr/dazyflow/drops/notion"
	_ "git.sr.ht/~klahr/dazyflow/drops/openai"
	_ "git.sr.ht/~klahr/dazyflow/drops/openmeteo"
	_ "git.sr.ht/~klahr/dazyflow/drops/rss"
	_ "git.sr.ht/~klahr/dazyflow/drops/secrets"
	_ "git.sr.ht/~klahr/dazyflow/drops/sheets"
	_ "git.sr.ht/~klahr/dazyflow/drops/shell"
	_ "git.sr.ht/~klahr/dazyflow/drops/slack"
	_ "git.sr.ht/~klahr/dazyflow/drops/smhi"
	_ "git.sr.ht/~klahr/dazyflow/drops/stripe"
	_ "git.sr.ht/~klahr/dazyflow/drops/transform"
	_ "git.sr.ht/~klahr/dazyflow/drops/trigger"
	_ "git.sr.ht/~klahr/dazyflow/drops/trigger/gform"
	_ "git.sr.ht/~klahr/dazyflow/drops/twilio"
	_ "git.sr.ht/~klahr/dazyflow/drops/value"
	_ "git.sr.ht/~klahr/dazyflow/drops/weather"
)
