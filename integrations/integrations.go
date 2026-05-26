// Package integrations is an umbrella import: depending on this package
// pulls in every built-in native drop via side-effect init()
// registration. Each subdirectory is one integration (or a bucket of
// standard-library drops); the catalog UI surfaces them grouped by the
// Manifest.Integration field.
package integrations

import (
	_ "git.sr.ht/~klahr/hazy-flow/integrations/ai"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/db"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/flow"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/git"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/github"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/gmail"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/io"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/net"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/notify"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/sheets"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/shell"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/slack"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/transform"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/trigger"
	_ "git.sr.ht/~klahr/hazy-flow/integrations/value"
)
