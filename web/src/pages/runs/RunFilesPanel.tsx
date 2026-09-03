// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useState } from "react";
import { Download, FileText } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../../api";
import { Button } from "../../components/ui/Button";
import { downloadBlob } from "../../lib/download";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { ICON } from "../../icons";
import { artifactName, type RunArtifact } from "../../lib/runArtifacts";

// The Files panel: the workspace files this run's steps wrote or read.
//
// A flow whose last step is "Save as file" has no value to show — the answer
// is a file, and until now the only trace of it on this page was a path string
// folded inside a step's output port, with the Files page as the next stop and
// no hint of which of its entries this run produced. This lists them with a
// download beside each.
//
// The bytes are served by the workspace file endpoint, which needs the bearer
// token, so each row fetches a blob and saves it rather than being a link.

export function RunFilesPanel({
  artifacts,
  tenant,
  workspace,
  token,
  // nodeLabel resolves a step id to its friendly name, shared with the
  // timeline so a file and its step read the same.
  nodeLabel,
}: {
  artifacts: RunArtifact[];
  tenant: string;
  workspace: string;
  token: string | null;
  nodeLabel: (nodeID: string) => string;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (artifacts.length === 0) return null;

  const download = async (a: RunArtifact) => {
    if (!token || !tenant || !workspace) return;
    setBusy(a.path);
    setError(null);
    try {
      const blob = await api.downloadWorkspaceFile(
        token,
        tenant,
        workspace,
        a.path,
      );
      downloadBlob(blob, artifactName(a));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setBusy(null);
    }
  };

  return (
    <>
      <h2 style={{ marginTop: "var(--space-4)" }}>{t("runDetail.files")}</h2>
      <div className="card run-files">
        {error && <ErrorNotice>{error}</ErrorNotice>}
        {artifacts.map((a) => (
          <div className="run-file-row" key={a.raw + a.nodeID + a.port}>
            <FileText size={ICON.sm} aria-hidden />
            <span className="run-file-name" title={a.raw}>
              {artifactName(a)}
            </span>
            <span className="run-file-meta">
              {t("runDetail.fileFrom", { label: nodeLabel(a.nodeID) })}
            </span>
            {a.ephemeral ? (
              // Nothing to offer: the run's scratch tree is reclaimed when it
              // finishes. Say so, rather than showing a button that 400s.
              <span className="run-file-gone">{t("runDetail.fileTemporary")}</span>
            ) : (
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void download(a)}
                loading={busy === a.path}
                disabled={!token || !tenant || !workspace}
                title={t("runDetail.downloadFile")}
              >
                <Download size={ICON.sm} />
                {t("common.download")}
              </Button>
            )}
          </div>
        ))}
      </div>
    </>
  );
}
