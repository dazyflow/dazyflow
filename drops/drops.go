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
	_ "git.sr.ht/~klahr/hazyflow/drops/claude"
	_ "git.sr.ht/~klahr/hazyflow/drops/db"
	_ "git.sr.ht/~klahr/hazyflow/drops/excel"
	_ "git.sr.ht/~klahr/hazyflow/drops/flow"
	_ "git.sr.ht/~klahr/hazyflow/drops/git"
	_ "git.sr.ht/~klahr/hazyflow/drops/github"
	_ "git.sr.ht/~klahr/hazyflow/drops/gmail"
	_ "git.sr.ht/~klahr/hazyflow/drops/homeassistant"
	_ "git.sr.ht/~klahr/hazyflow/drops/io"
	_ "git.sr.ht/~klahr/hazyflow/drops/net"
	_ "git.sr.ht/~klahr/hazyflow/drops/notify"
	_ "git.sr.ht/~klahr/hazyflow/drops/notion"
	_ "git.sr.ht/~klahr/hazyflow/drops/openai"
	_ "git.sr.ht/~klahr/hazyflow/drops/secrets"
	_ "git.sr.ht/~klahr/hazyflow/drops/sheets"
	_ "git.sr.ht/~klahr/hazyflow/drops/shell"
	_ "git.sr.ht/~klahr/hazyflow/drops/slack"
	_ "git.sr.ht/~klahr/hazyflow/drops/stripe"
	_ "git.sr.ht/~klahr/hazyflow/drops/transform"
	_ "git.sr.ht/~klahr/hazyflow/drops/trigger"
	_ "git.sr.ht/~klahr/hazyflow/drops/trigger/gform"
	_ "git.sr.ht/~klahr/hazyflow/drops/value"
)
