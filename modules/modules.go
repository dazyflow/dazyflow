// Package modules is an umbrella import: depending on this package pulls in
// every built-in native module via side-effect init() registration.
package modules

import (
	_ "git.sr.ht/~klahr/hazy-flow/modules/ai"
	_ "git.sr.ht/~klahr/hazy-flow/modules/flow"
	_ "git.sr.ht/~klahr/hazy-flow/modules/git"
	_ "git.sr.ht/~klahr/hazy-flow/modules/io"
	_ "git.sr.ht/~klahr/hazy-flow/modules/net"
	_ "git.sr.ht/~klahr/hazy-flow/modules/notify"
	_ "git.sr.ht/~klahr/hazy-flow/modules/trigger"
)
