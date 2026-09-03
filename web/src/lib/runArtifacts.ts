// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The files a run touched.
//
// A step that writes a file emits the path on its output port rather than the
// bytes (core.Ref.Ref instead of Ref.Inline), so a flow ending in "Save as
// file", "Write Excel" or "Merge PDFs" has a result the Result panel can only
// describe as a string. The file itself is right there in the workspace, and
// the workspace file endpoints can already serve it — this turns those refs
// into a list with a download beside each one.
//
// No new storage: an artifact IS a workspace file. That also sets the one real
// limit — a scratch:// path lives in the run's ephemeral tree and is reclaimed
// when the run finishes, so it is listed as gone rather than offered as a
// download the server would refuse.

import type { JobRecord, Ref } from "../types";

// SCRATCH_SCHEME marks a path in the run's per-run scratch tree. Mirrors
// drops/internal/sandbox.Scheme.
const SCRATCH_SCHEME = "scratch://";

// WORKSPACE_SCHEME is a redundant explicit spelling of "workspace-relative".
// A step echoes its path param straight into its output ref, so a flow built
// by copying a step example from before the prefix was retired still emits it
// — the resolver strips it on the way to disk (drops/internal/sandbox), and
// this strips it on the way to a download URL.
const WORKSPACE_SCHEME = "workspace://";

// The daemon hides its scratch subtree from every workspace file operation
// (daemon/httpfiles.go isScratch), so a ref pointing into it is not
// downloadable either. Mirrors daemon.scratchDirName.
const SCRATCH_DIR = ".scratch";

export type RunArtifact = {
  // Which step emitted it, and on which port.
  nodeID: string;
  port: string;
  // path is workspace-relative and ready for the file-download endpoint.
  // Empty when the ref is ephemeral — there is nothing to ask for.
  path: string;
  // raw is the ref exactly as the step emitted it, so the row can show the
  // scratch:// spelling the flow author typed.
  raw: string;
  mime?: string;
  // ephemeral: the bytes lived in the run's scratch tree and are gone. Listed
  // anyway — "the file you asked for was temporary" is the answer to why
  // there is nothing to download, and silence is not.
  ephemeral: boolean;
};

// artifactName is the basename shown in the list — the part a person
// recognises. Falls back to the whole ref for a path that ends in a
// separator or is otherwise nameless.
export function artifactName(a: RunArtifact): string {
  const p = (a.path || a.raw).replace(/\/+$/, "");
  const cut = p.lastIndexOf("/");
  return (cut >= 0 ? p.slice(cut + 1) : p) || p;
}

// normalizeRef turns a step's ref string into a workspace-relative path, or
// null when it does not name a downloadable workspace file. Rejects the same
// shapes the daemon's cleanWorkspaceRel rejects — absolute paths and "../"
// escapes — so the list never offers a download that is bound to 400.
function normalizeRef(raw: string): string | null {
  let p = raw.trim();
  if (p === "") return null;
  if (p.startsWith(WORKSPACE_SCHEME)) p = p.slice(WORKSPACE_SCHEME.length);
  // A ref carrying any other scheme is not a sandbox path at all (an http
  // URL, a blob handle); it is not this panel's business.
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(p)) return null;
  if (p.startsWith("/")) return null;
  const parts: string[] = [];
  for (const seg of p.split("/")) {
    if (seg === "" || seg === ".") continue;
    if (seg === "..") return null;
    parts.push(seg);
  }
  if (parts.length === 0) return null;
  if (parts[0] === SCRATCH_DIR) return null;
  return parts.join("/");
}

// collectArtifacts walks the run's step outputs in execution order and
// returns the files they named, first writer wins. Deduped by path: a file
// written by one step and read back by the next is one file, and listing it
// twice would read as two.
export function collectArtifacts(nodes: JobRecord[]): RunArtifact[] {
  const out: RunArtifact[] = [];
  const seen = new Set<string>();
  for (const n of nodes) {
    const output: Record<string, Ref> = n.Result?.output ?? {};
    for (const port of Object.keys(output)) {
      const raw = output[port]?.ref;
      if (!raw || typeof raw !== "string" || raw.trim() === "") continue;
      const ephemeral = raw.trim().startsWith(SCRATCH_SCHEME);
      const path = ephemeral ? "" : normalizeRef(raw);
      if (path === null) continue;
      const key = ephemeral ? SCRATCH_SCHEME + raw.trim() : path;
      if (seen.has(key)) continue;
      seen.add(key);
      out.push({
        nodeID: n.NodeID,
        port,
        path,
        raw: raw.trim(),
        mime: output[port]?.mime,
        ephemeral,
      });
    }
  }
  return out;
}
