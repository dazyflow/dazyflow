// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	_ "github.com/dazyflow/dazyflow/drops/claude"
	_ "github.com/dazyflow/dazyflow/drops/datetime"
	_ "github.com/dazyflow/dazyflow/drops/db"
	_ "github.com/dazyflow/dazyflow/drops/discord"
	_ "github.com/dazyflow/dazyflow/drops/drive"
	_ "github.com/dazyflow/dazyflow/drops/elks"
	_ "github.com/dazyflow/dazyflow/drops/encoding"
	_ "github.com/dazyflow/dazyflow/drops/excel"
	_ "github.com/dazyflow/dazyflow/drops/flow"
	_ "github.com/dazyflow/dazyflow/drops/fortnox"
	_ "github.com/dazyflow/dazyflow/drops/gcal"
	_ "github.com/dazyflow/dazyflow/drops/gemini"
	_ "github.com/dazyflow/dazyflow/drops/geo"
	_ "github.com/dazyflow/dazyflow/drops/git"
	_ "github.com/dazyflow/dazyflow/drops/github"
	_ "github.com/dazyflow/dazyflow/drops/gmail"
	_ "github.com/dazyflow/dazyflow/drops/homeassistant"
	_ "github.com/dazyflow/dazyflow/drops/io"
	_ "github.com/dazyflow/dazyflow/drops/klarna"
	_ "github.com/dazyflow/dazyflow/drops/mailbox"
	_ "github.com/dazyflow/dazyflow/drops/mqtt"
	_ "github.com/dazyflow/dazyflow/drops/net"
	_ "github.com/dazyflow/dazyflow/drops/notify"
	_ "github.com/dazyflow/dazyflow/drops/notion"
	_ "github.com/dazyflow/dazyflow/drops/nshift"
	_ "github.com/dazyflow/dazyflow/drops/ollama"
	_ "github.com/dazyflow/dazyflow/drops/openai"
	_ "github.com/dazyflow/dazyflow/drops/openmeteo"
	_ "github.com/dazyflow/dazyflow/drops/roaring"
	_ "github.com/dazyflow/dazyflow/drops/rss"
	_ "github.com/dazyflow/dazyflow/drops/runner"
	_ "github.com/dazyflow/dazyflow/drops/secrets"
	_ "github.com/dazyflow/dazyflow/drops/sheets"
	_ "github.com/dazyflow/dazyflow/drops/shell"
	_ "github.com/dazyflow/dazyflow/drops/slack"
	_ "github.com/dazyflow/dazyflow/drops/smhi"
	_ "github.com/dazyflow/dazyflow/drops/stripe"
	_ "github.com/dazyflow/dazyflow/drops/transform"
	_ "github.com/dazyflow/dazyflow/drops/trigger"
	_ "github.com/dazyflow/dazyflow/drops/trigger/gform"
	_ "github.com/dazyflow/dazyflow/drops/twilio"
	_ "github.com/dazyflow/dazyflow/drops/value"
	_ "github.com/dazyflow/dazyflow/drops/weather"
)
