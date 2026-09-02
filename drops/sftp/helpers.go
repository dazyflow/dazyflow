// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sftp holds the drops that move files over SFTP — list a remote
// folder, download a file, upload one. It is the vendor-neutral counterpart
// to Upload to Drive: a protocol rather than an account, so it reaches a
// bank's drop box, a supplier's feed and a server you run yourself with one
// connector and no app to register.
//
// Corporate integration still largely means "a file lands on an SFTP server
// at 03:00" — bank statements, payroll, EDI, price lists. That is the shape
// this package serves, and why List files carries a watermark: a poll that
// re-processes yesterday's statement is worse than one that finds nothing.
package sftp

import (
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/internal/sftputil"
)

// integration is the label every drop here shares — the name of the page a
// tenant configures once, and the key its stored connection hangs off.
const integration = "SFTP"

// brandColor is shared by every step in the app so the cards read as one
// group on the canvas.
const brandColor = "#7c3aed"

// connectionFields is the SFTP server, configured once on the integration
// page and injected into every node's params at run time
// (injectConnectionDefaults) — so flows carry only the per-transfer fields,
// and neither the password nor the private key ever lands in a graph.
//
// Every drop in the integration MUST declare this same slice: the connection
// UI takes the fields from whichever drop it finds first, so a drop declaring
// a subset would render a page missing whatever it left out.
//
// One connection per tenant, like Postgres and Mailbox. Someone with a bank
// drop box AND a supplier feed needs two, which this shape doesn't give them
// — the named-credential store behind drops/git is the pattern that would,
// and the honest upgrade path if people ask.
func connectionFields() []core.ConnectionField {
	return []core.ConnectionField{
		{Key: "host", Label: "Server", Required: true, Placeholder: "sftp.example.com"},
		{Key: "port", Label: "Port", Placeholder: "22"},
		{Key: "username", Label: "Username", Required: true},
		{Key: "password", Label: "Password", Secret: true, Help: "Fill in either a password or a private key below — whichever the server expects."},
		{Key: "private_key", Label: "SSH private key", Secret: true, Help: "The whole key including the BEGIN/END lines. Preferred over a password where the server allows it."},
		{Key: "passphrase", Label: "Key passphrase", Secret: true, Help: "Only if the private key is encrypted."},
		{Key: "fingerprint", Label: "Host key fingerprint", Placeholder: "SHA256:…", Help: "Proves you're talking to the right server. Leave it blank and Test connection will show you the server's fingerprint to paste in."},
		{Key: "known_hosts", Label: "known_hosts entry", Help: "An alternative to the fingerprint, if you already have an OpenSSH known_hosts line for this server."},
		{Key: "directory", Label: "Folder", Placeholder: "/incoming", Help: "The remote folder the steps work in by default. A step can point at another one."},
	}
}

// configFromJob assembles the SFTP connection from the params the engine
// injected. `directory` is declared as a param as well as a connection
// field, so a step can point at another folder while everything else comes
// from the connection — injectConnectionDefaults leaves an author's per-step
// value alone.
func configFromJob(job core.Job) (sftputil.Config, error) {
	host := strings.TrimSpace(params.StringDefault(job.Params, "host", ""))
	if host == "" {
		return sftputil.Config{}, fmt.Errorf("no SFTP server connected — set one up on the SFTP integration page")
	}
	// ConnectionFields inject the port as a string ("22"); a graph saved
	// before the field existed may carry it as a number. Try the string form
	// first, then the numeric one.
	portStr := strings.TrimSpace(params.StringDefault(job.Params, "port", ""))
	if portStr == "" {
		if n := params.IntDefault(job.Params, "port", 0); n > 0 {
			portStr = strconv.Itoa(n)
		}
	}
	port, err := sftputil.ParsePort(portStr)
	if err != nil {
		return sftputil.Config{}, err
	}
	return sftputil.Config{
		Host:        host,
		Port:        port,
		Username:    params.StringDefault(job.Params, "username", ""),
		Password:    params.StringDefault(job.Params, "password", ""),
		PrivateKey:  params.StringDefault(job.Params, "private_key", ""),
		Passphrase:  params.StringDefault(job.Params, "passphrase", ""),
		KnownHosts:  params.StringDefault(job.Params, "known_hosts", ""),
		Fingerprint: params.StringDefault(job.Params, "fingerprint", ""),
		Directory:   remoteDir(job),
	}, nil
}

// remoteDir is the folder a step works in: the param when set, otherwise
// whatever the connection injected, otherwise the account's home directory
// (which is what "." means to every SFTP server).
func remoteDir(job core.Job) string {
	if d := strings.TrimSpace(params.StringDefault(job.Params, "directory", "")); d != "" {
		return d
	}
	return "."
}

// fileRecord is one remote file, as a flow works with it.
//
// `path` is the full remote path, so it wires straight into Download file
// without the author having to rebuild it from the folder and the name —
// the same reasoning behind Search emails emitting ids that Read email
// takes. `modified` is RFC3339 so a Date step or a Compare can use it.
func fileRecord(dir string, info os.FileInfo) map[string]any {
	return map[string]any{
		"name":     info.Name(),
		"path":     path.Join(dir, info.Name()),
		"size":     info.Size(),
		"modified": info.ModTime().UTC().Format(time.RFC3339),
	}
}

// matchesPattern reports whether name passes the step's filter. An empty
// pattern keeps everything; otherwise it is a shell glob ("*.csv"), matched
// case-insensitively because half the servers in this world are Windows.
func matchesPattern(name, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return true
	}
	ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	if err != nil {
		// A malformed glob keeps nothing rather than everything: a filter that
		// silently stopped filtering would hand a flow files it was told to
		// leave alone.
		return false
	}
	return ok
}

// sortByModified orders records oldest-first, which is the order a feed
// should be processed in — yesterday's statement before today's.
func sortByModified(rows []map[string]any) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, _ := rows[i]["modified"].(string)
		b, _ := rows[j]["modified"].(string)
		if a != b {
			return a < b
		}
		an, _ := rows[i]["name"].(string)
		bn, _ := rows[j]["name"].(string)
		return an < bn
	})
}

// sortStrings is sort.Strings, wrapped so sftp_list_files does not need its
// own sort import for one call.
func sortStrings(s []string) { sort.Strings(s) }

// resolveRemotePath works out which file a step was pointed at. It accepts a
// path (text, e.g. ${item.path} inside a For each) or a List files record
// wired straight in — in which case its `path` is used, so the obvious drag
// just works. ok=false means the input carried something that can't be a
// path at all.
func resolveRemotePath(job core.Job) (string, bool) {
	fallback := params.StringDefault(job.Params, "path", "")
	in, present := job.Input["path"]
	if !present || in.Inline == nil {
		return fallback, true
	}
	recordPath := func(v any) string {
		m, isMap := v.(map[string]any)
		if !isMap {
			return ""
		}
		if s, _ := m["path"].(string); s != "" {
			return s
		}
		// A record with only a name is still usable — join it onto the folder
		// the step is working in.
		if s, _ := m["name"].(string); s != "" {
			return path.Join(remoteDir(job), s)
		}
		return ""
	}
	switch v := in.Inline.(type) {
	case string:
		if v != "" {
			return v, true
		}
		return fallback, true
	case []byte:
		if len(v) > 0 {
			return string(v), true
		}
		return fallback, true
	case map[string]any:
		if s := recordPath(v); s != "" {
			return s, true
		}
		return "", false
	case []any:
		// The whole file list: take the first entry.
		for _, item := range v {
			if s := recordPath(item); s != "" {
				return s, true
			}
		}
		return "", false
	default:
		return "", false
	}
}
