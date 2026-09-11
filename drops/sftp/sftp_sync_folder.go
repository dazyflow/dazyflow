// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sftp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/drops/internal/sandbox"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/internal/sshutil"
)

// The answer to "do you support rsync". Not the protocol — that is a binary
// that has to exist on both ends, and handing it a key would mean writing the
// key to disk, which nothing in this codebase does. What people want from it is
// a folder kept in step, which is this: compare both sides, move only what
// differs, and leave the rest alone.
//
// The comparison is size plus modified time, which is what rsync itself uses by
// default. The delta algorithm is the part that pays off on a large file that
// mostly did not change — a VM image, a mailbox — and not on the shapes this
// package serves, where a feed drops a new file each night.
//
// Setting the modified time on the copy is what makes the second run cheap:
// without it every file looks changed for ever and the sync is just a download
// loop with extra steps.
const (
	syncDown = "download"
	syncUp   = "upload"
)

type syncEntry struct {
	rel      string
	size     int64
	modified time.Time
}

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "sftp_sync_folder",
			Version:  "1.0",
			Label:    integration,
			Subtitle: "Sync folder",
			Summary:  "Keep a workspace folder and a folder on an SFTP server in step.",
			Description: "Copy a whole folder between an SFTP server and your workspace, moving only the files that differ. Everything already on both sides at the same size and time is left alone, so the first run does the work and the ones after it are nearly free.\n\n" +
				"Choose the direction: bring a folder down from the server, or push one up to it. Subfolders come along by default, and a pattern like \"*.csv\" narrows what counts.\n\n" +
				"'Files' carries just what this run actually moved, so a step after it acts on the changes rather than the whole folder — which is the difference between this and listing a folder and looping over all of it.\n\n" +
				"Deleting what is no longer on the other side is off by default. A mirror that deletes is a genuine mirror, and it is also the setting that can empty a folder because a pattern was wrong, so it has to be asked for.\n\n" +
				"This is what to reach for instead of rsync: rsync's block-by-block trick pays off on a large file that mostly didn't change, and a feed that drops a new file each night has nothing for it to save.",
			Integration: integration,
			Category:    "network",
			Icon:        "folder-sync",
			Color:       brandColor,
			Provider:    "internal",
			Tags: []string{"sftp", "ssh", "sync", "mirror", "rsync", "folder",
				"directory", "tree", "backup", "copy", "feed"},
			Examples: []core.ParamsExample{
				{
					Title:  "Bring down everything new in a feed",
					Params: json.RawMessage(`{"remote":"/incoming","local":"feeds","pattern":"*.csv"}`),
					Notes:  "Only the files that changed are transferred; 'Files' lists them for the next step.",
				},
				{
					Title:  "Publish a folder to a supplier, and keep it exact",
					Params: json.RawMessage(`{"direction":"upload","local":"outgoing","remote":"/to-supplier","delete":true}`),
					Notes:  "With 'Delete what is not on the other side' on, a file removed locally is removed there too.",
				},
			},
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			ConnectionFields: connectionFields(),
			Outputs: []core.Port{
				{Port: "files", Label: "Files moved", MIME: []string{"application/json"},
					Example: json.RawMessage(`[
						{"name":"statement-2026-02-12.csv","path":"/incoming/statement-2026-02-12.csv","size":18244,"modified":"2026-02-12T03:00:04Z"}
					]`)},
				{Port: "transferred", Label: "Moved", MIME: []string{"text/plain"}, Example: json.RawMessage(`"3"`)},
				{Port: "skipped", Label: "Already in step", MIME: []string{"text/plain"}, Example: json.RawMessage(`"128"`)},
				{Port: "deleted", Label: "Deleted", MIME: []string{"text/plain"}, Example: json.RawMessage(`"0"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"required":["local"],
				"properties":{
					"account":{"type":"string","title":"Server","format":"ssh-account","x_blank_connection":"sftp","description":"Which saved server to use. Leave blank to use the single connection from the SFTP integration page. Manage them on the Servers page; the SSH step picks from the same list."},
					"direction":{"type":"string","title":"Which way","default":"download","enum":["download","upload"],"enumNames":["Server → workspace","Workspace → server"],"description":"Whether to bring the server's folder down into the workspace, or push the workspace folder up to the server. The side you copy FROM is never changed."},
					"remote":{"type":"string","title":"Folder on the server","examples":["/incoming"],"description":"The remote folder. Leave blank to use the folder set on the server."},
					"local":{"type":"string","title":"Folder in the workspace","format":"workspace-dir","description":"The workspace folder this keeps in step with the server's."},
					"pattern":{"type":"string","title":"Only files like","examples":["*.csv"],"description":"Shell-style pattern a file name must match, e.g. \"*.csv\". Case-insensitive. Leave blank for every file. Files that don't match are ignored on both sides — including by 'Delete what is not on the other side', which will not remove something it was told not to look at."},
					"subfolders":{"type":"boolean","title":"Include subfolders","default":true,"description":"Copy the folder's whole tree. Turn it off to sync only the files sitting directly in it."},
					"delete":{"type":"boolean","title":"Delete what is not on the other side","default":false,"description":"Remove files from the destination that are no longer on the source, making this a true mirror. Off by default: it is also what empties a folder when a pattern is wrong, and the files it removes are not recoverable from here."},
					"limit":{"type":"integer","title":"Max files per run","default":500,"minimum":1,"maximum":5000,"description":"How many files one run may transfer. What is left over is picked up by the next run — a sync resumes by its nature, because everything already moved is skipped."},
					"timeout_ms":{"type":"integer","default":300000,"minimum":1,"description":"Hard deadline for the whole sync, in milliseconds."}
				}
			}`),
			// The end state is the same however many times it runs.
			Idempotent: true,
		},
		Execute: executeSFTPSync,
	})
}

func executeSFTPSync(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	cfg, err := sftpConfig(ctx, job)
	if err != nil {
		return params.Err(job, "not_connected", err.Error()), nil
	}
	direction := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "direction", syncDown)))
	if direction == "" {
		direction = syncDown
	}
	if direction != syncDown && direction != syncUp {
		return params.Err(job, "bad_param", fmt.Sprintf("'which way' is %s, which is neither %s nor %s", direction, syncDown, syncUp)), nil
	}
	local := strings.TrimSpace(params.StringDefault(job.Params, "local", ""))
	if local == "" {
		return params.Err(job, "bad_param", "'folder in the workspace' is required — name the workspace folder to keep in step"), nil
	}
	remote := strings.TrimSpace(params.StringDefault(job.Params, "remote", ""))
	if remote == "" {
		remote = cfg.Directory
	}
	if remote == "" {
		remote = "."
	}
	pattern := params.StringDefault(job.Params, "pattern", "")
	subfolders := params.BoolDefault(job.Params, "subfolders", true)
	limit := params.ClampInt(params.IntDefault(job.Params, "limit", 500), 1, 5000)

	root, base, serr := sandbox.OpenRoot(job, local)
	if serr != nil {
		if sandbox.IsEscape(serr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("%q escapes its sandbox root", local)), nil
		}
		return params.Err(job, "no_sandbox", serr.Error()), nil
	}
	defer root.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Duration(params.TimeoutMS(job, 300000))*time.Millisecond)
	defer cancel()

	client, err := sshutil.Dial(ctx, cfg)
	if err != nil {
		return params.Err(job, "sftp_error", err.Error()), nil
	}
	defer client.Close()

	// The destination has to exist before anything is compared against it —
	// syncing into a folder nobody has made yet is the normal first run.
	if direction == syncDown {
		if merr := root.MkdirAll(base, 0o755); merr != nil && !sandbox.IsEscape(merr) {
			return params.Err(job, "io", fmt.Sprintf("create %q in the workspace: %v", local, merr)), nil
		}
	} else if merr := client.MkdirAll(remote); merr != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't create %q on the server — check the account may write there (%v)", remote, merr)), nil
	}

	remoteFiles, rerr := listRemoteTree(client, remote, subfolders, pattern)
	if rerr != nil {
		return params.Err(job, "sftp_error", fmt.Sprintf("couldn't read %q on the server — check the path and that the account may read it (%v)", remote, rerr)), nil
	}
	localFiles, lerr := listLocalTree(root, base, subfolders, pattern)
	if lerr != nil {
		return params.Err(job, "io", fmt.Sprintf("couldn't read %q in the workspace: %v", local, lerr)), nil
	}

	source, dest := remoteFiles, localFiles
	if direction == syncUp {
		source, dest = localFiles, remoteFiles
	}

	// Oldest first, so a run that hits the cap leaves the NEWEST behind: a feed
	// has to be worked in arrival order or the backlog never catches up.
	todo := make([]syncEntry, 0, len(source))
	skipped := 0
	for _, src := range sortedEntries(source) {
		if inStep(src, dest[src.rel]) {
			skipped++
			continue
		}
		todo = append(todo, src)
	}
	if len(todo) > limit {
		todo = todo[:limit]
	}

	moved := make([]map[string]any, 0, len(todo))
	for _, e := range todo {
		if ctx.Err() != nil {
			return params.Err(job, "timeout", fmt.Sprintf("the sync ran out of time after %d of %d files — raise the deadline, or lower 'max files per run' and let it catch up over several runs", len(moved), len(todo))), nil
		}
		var terr error
		if direction == syncDown {
			terr = syncDownOne(client, root, remote, base, e)
		} else {
			terr = syncUpOne(client, root, remote, base, e)
		}
		if terr != nil {
			return params.Err(job, "sftp_error", fmt.Sprintf("%q: %v — %d files had already been copied and will be skipped next run", e.rel, terr, len(moved))), nil
		}
		moved = append(moved, map[string]any{
			"name":     path.Base(e.rel),
			"path":     path.Join(remote, e.rel),
			"size":     e.size,
			"modified": e.modified.UTC().Format(time.RFC3339),
		})
	}

	deleted := 0
	if params.BoolDefault(job.Params, "delete", false) {
		for _, d := range sortedEntries(dest) {
			if _, stillThere := source[d.rel]; stillThere {
				continue
			}
			var derr error
			if direction == syncDown {
				derr = root.Remove(path.Join(base, d.rel))
			} else {
				derr = client.Remove(path.Join(remote, d.rel))
			}
			if derr != nil {
				return params.Err(job, "sftp_error", fmt.Sprintf("couldn't delete %q: %v", d.rel, derr)), nil
			}
			deleted++
		}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"files":       {MIME: "application/json", Inline: moved, Headers: []string{"name", "path", "size", "modified"}},
			"transferred": {MIME: "text/plain", Inline: fmt.Sprint(len(moved))},
			"skipped":     {MIME: "text/plain", Inline: fmt.Sprint(skipped)},
			"deleted":     {MIME: "text/plain", Inline: fmt.Sprint(deleted)},
		},
	}, nil
}

// inStep is rsync's own default test: same size, same modified time. SFTP
// reports whole seconds, so the comparison is to the second — a finer one would
// call every file changed for ever.
func inStep(src, dst syncEntry) bool {
	if dst.rel == "" {
		return false
	}
	return src.size == dst.size && src.modified.Unix() == dst.modified.Unix()
}

func sortedEntries(m map[string]syncEntry) []syncEntry {
	out := make([]syncEntry, 0, len(m))
	for _, e := range m {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].modified.Equal(out[j].modified) {
			return out[i].rel < out[j].rel
		}
		return out[i].modified.Before(out[j].modified)
	})
	return out
}

func listRemoteTree(client *sshutil.Client, base string, subfolders bool, pattern string) (map[string]syncEntry, error) {
	out := map[string]syncEntry{}
	var walk func(dir, prefix string) error
	walk = func(dir, prefix string) error {
		entries, err := client.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, info := range entries {
			rel := path.Join(prefix, info.Name())
			if info.IsDir() {
				if !subfolders {
					continue
				}
				if err := walk(path.Join(dir, info.Name()), rel); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() || !matchesPattern(info.Name(), pattern) {
				continue
			}
			out[rel] = syncEntry{rel: rel, size: info.Size(), modified: info.ModTime()}
		}
		return nil
	}
	if err := walk(base, ""); err != nil {
		return nil, err
	}
	return out, nil
}

func listLocalTree(root *os.Root, base string, subfolders bool, pattern string) (map[string]syncEntry, error) {
	out := map[string]syncEntry{}
	start := base
	if start == "" {
		start = "."
	}
	err := fs.WalkDir(root.FS(), start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A folder that isn't there yet is an empty side, not a failure:
			// the first run in either direction starts from nothing.
			if p == start && os.IsNotExist(err) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			if p != start && !subfolders {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := relativeTo(start, p)
		if rerr != nil || !matchesPattern(path.Base(p), pattern) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		out[rel] = syncEntry{rel: rel, size: info.Size(), modified: info.ModTime()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func relativeTo(base, p string) (string, error) {
	if base == "." {
		return p, nil
	}
	rel := strings.TrimPrefix(p, base+"/")
	if rel == p {
		return "", fmt.Errorf("%q is not under %q", p, base)
	}
	return rel, nil
}

func syncDownOne(client *sshutil.Client, root *os.Root, remote, base string, e syncEntry) error {
	if e.size > maxDownloadBytes {
		return fmt.Errorf("%d MiB, over the %d MiB limit", e.size>>20, int64(maxDownloadBytes)>>20)
	}
	dest := path.Join(base, e.rel)
	if dir := path.Dir(dest); dir != "." && dir != "" {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	src, err := client.Open(path.Join(remote, e.rel))
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := root.Create(dest)
	if err != nil {
		return err
	}
	// LimitReader as well as the size check: the size a server reports is a
	// claim, and a stream that keeps going must not keep filling the disk.
	_, cerr := io.Copy(dst, io.LimitReader(src, maxDownloadBytes+1))
	closeErr := dst.Close()
	if cerr != nil {
		return cerr
	}
	if closeErr != nil {
		return closeErr
	}
	// The copy carries the source's time, which is the whole basis of the next
	// run's comparison.
	return root.Chtimes(dest, e.modified, e.modified)
}

func syncUpOne(client *sshutil.Client, root *os.Root, remote, base string, e syncEntry) error {
	dest := path.Join(remote, e.rel)
	if dir := path.Dir(dest); dir != "." && dir != "" {
		if err := client.MkdirAll(dir); err != nil {
			return err
		}
	}
	src, err := root.Open(path.Join(base, e.rel))
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := client.Create(dest)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(dst, src)
	closeErr := dst.Close()
	if cerr != nil {
		return cerr
	}
	if closeErr != nil {
		return closeErr
	}
	return client.Chtimes(dest, e.modified, e.modified)
}
