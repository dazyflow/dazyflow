// Package drops is an umbrella import: depending on this package pulls in
// every built-in native (Go, compiled-in) drop via side-effect init()
// registration. Each subdirectory is one bucket of drops — some
// vendor-flavoured (slack, github, git), most standard-library (transform,
// flow, value, trigger, io, ...). The catalog UI groups them at runtime by
// the Manifest.Integration / Manifest.Category fields, independent of this
// directory layout. (The scripted, embedded first-party drops live in
// ../officialdrops; the product's connectable "integrations" + OAuth
// connections are a separate concept defined by integration manifests.)
package drops

import (
	_ "git.sr.ht/~klahr/hazy-flow/drops/db"
	_ "git.sr.ht/~klahr/hazy-flow/drops/flow"
	_ "git.sr.ht/~klahr/hazy-flow/drops/git"
	_ "git.sr.ht/~klahr/hazy-flow/drops/github"
	_ "git.sr.ht/~klahr/hazy-flow/drops/io"
	_ "git.sr.ht/~klahr/hazy-flow/drops/net"
	_ "git.sr.ht/~klahr/hazy-flow/drops/notify"
	_ "git.sr.ht/~klahr/hazy-flow/drops/secrets"
	_ "git.sr.ht/~klahr/hazy-flow/drops/shell"
	_ "git.sr.ht/~klahr/hazy-flow/drops/slack"
	_ "git.sr.ht/~klahr/hazy-flow/drops/transform"
	_ "git.sr.ht/~klahr/hazy-flow/drops/trigger"
	_ "git.sr.ht/~klahr/hazy-flow/drops/value"
)
