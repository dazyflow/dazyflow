// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, Plug, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "../../components/ui/Button";
import { useAuth } from "../../auth";
import { api } from "../../api";
import type { Runner, RunnerProbe } from "../../types";
import { explainApiError } from "../../lib/explainApiError";
import { ErrorNotice } from "../../components/ui/ErrorNotice";
import { EmptyState } from "../../components/ui/EmptyState";
import { Loading } from "../../components/ui/Loading";
import { Notice } from "../../components/ui/Notice";
import { ICON } from "../../icons";
import { formatDateTime } from "../../lib/datetime";

// AdminRunners manages the org's runners — its own code, on its own hardware,
// reachable as a step in its flows.
//
// Two things make this page different from the other credential pages:
//
//   The private key is write-only. It goes in and is never read back, so
//   editing a runner means re-pasting it. That is stated on the form rather
//   than discovered.
//
//   A runner can be registered and NOT working. Registration dials, and a
//   runner that is down would otherwise vanish from the palette with nothing
//   to explain it, so the daemon keeps it listed as unreachable and this page
//   is where the reason lives.
export function AdminRunners() {
  const { t } = useTranslation();
  const { token } = useAuth();
  const [runners, setRunners] = useState<Runner[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  // The add/replace form.
  const [name, setName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [serverCA, setServerCA] = useState("");
  const [clientCert, setClientCert] = useState("");
  const [clientKey, setClientKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [probe, setProbe] = useState<RunnerProbe | null>(null);
  const [probing, setProbing] = useState(false);

  const load = useCallback(() => {
    if (!token) return;
    setLoading(true);
    api
      .listRunners(token)
      .then((r) => setRunners(r.runners ?? []))
      .catch((e) => setError(explainApiError(e, t)))
      .finally(() => setLoading(false));
  }, [token, t]);

  useEffect(() => {
    load();
  }, [load]);

  const body = () => ({
    endpoint: endpoint.trim(),
    server_ca_pem: serverCA.trim(),
    client_cert_pem: clientCert.trim(),
    client_key_pem: clientKey.trim(),
  });

  const clearForm = () => {
    setName("");
    setEndpoint("");
    setServerCA("");
    setClientCert("");
    setClientKey("");
    setProbe(null);
  };

  const save = async () => {
    if (!token) return;
    setSaving(true);
    setError(null);
    try {
      await api.putRunner(token, name.trim(), body());
      clearForm();
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setSaving(false);
    }
  };

  // Test before saving. The result reports the certificate subject even when
  // the connection fails, because confirming WHO you are about to trust is the
  // point — an address answering tells you much less.
  const test = async () => {
    if (!token) return;
    setProbing(true);
    setProbe(null);
    try {
      setProbe(await api.testRunner(token, name.trim() || "unnamed", body()));
    } catch (e) {
      setError(explainApiError(e, t));
    } finally {
      setProbing(false);
    }
  };

  const remove = async (runnerName: string) => {
    if (!token) return;
    setError(null);
    try {
      await api.deleteRunner(token, runnerName);
      load();
    } catch (e) {
      setError(explainApiError(e, t));
    }
  };

  const complete =
    name.trim() !== "" &&
    endpoint.trim() !== "" &&
    serverCA.trim() !== "" &&
    clientCert.trim() !== "" &&
    clientKey.trim() !== "";

  return (
    <div>
      <div className="page-title">
        <div>
          <h1>
            <Plug size={ICON.xl} />
            {t("runners.title")}
          </h1>
          <div className="sub">{t("runners.subtitle")}</div>
        </div>
      </div>

      {error && <ErrorNotice>{error}</ErrorNotice>}

      {loading ? (
        <Loading />
      ) : runners.length === 0 ? (
        <EmptyState icon={Plug} title={t("runners.emptyTitle")}>
          {t("runners.emptyBody")}
        </EmptyState>
      ) : (
        <div className="card runner-list">
          <table className="run-table">
            <thead>
              <tr>
                <th>{t("common.name")}</th>
                <th>{t("runners.endpointLabel")}</th>
                <th>{t("common.status")}</th>
                <th>{t("runners.colSteps")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {runners.map((r) => (
                <tr key={r.name}>
                  <td>{r.name}</td>
                  <td className="muted runner-endpoint">{r.endpoint}</td>
                  <td>
                    <RunnerStateChip runner={r} />
                    {r.error && <div className="desc runner-error">{r.error}</div>}
                  </td>
                  <td className="muted runner-steps">
                    {r.drops?.length ? r.drops.join(" · ") : t("runners.noSteps")}
                  </td>
                  <td className="runner-actions">
                    <Button
                      variant="ghost"
                      className="danger"
                      onClick={() => void remove(r.name)}
                      title={t("runners.remove")}
                      aria-label={t("runners.remove")}
                    >
                      <Trash2 size={ICON.sm} />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {runners.some((r) => r.expiring_soon) && (
        <Notice className="runner-expiry">
          <AlertTriangle size={ICON.sm} className="icon-lede" />
          {t("runners.expiringSoon", {
            names: runners
              .filter((r) => r.expiring_soon)
              .map((r) => r.name)
              .join(", "),
          })}
        </Notice>
      )}

      <div className="card runner-form">
        <h2 className="runner-form-head">{t("runners.addTitle")}</h2>
        <p className="desc">{t("runners.addIntro")}</p>

        <div className="sf-field">
          <label htmlFor="runner-name">{t("common.name")}</label>
          <input
            id="runner-name"
            type="text"
            value={name}
            placeholder="invoices"
            onChange={(e) => setName(e.target.value)}
          />
          <div className="desc">{t("runners.nameDesc")}</div>
        </div>

        <div className="sf-field">
          <label htmlFor="runner-endpoint">{t("runners.endpointLabel")}</label>
          <input
            id="runner-endpoint"
            type="text"
            value={endpoint}
            placeholder="runner.example.internal:9000"
            onChange={(e) => setEndpoint(e.target.value)}
          />
          <div className="desc">{t("runners.endpointDesc")}</div>
        </div>

        <div className="sf-field">
          <label htmlFor="runner-server-ca">{t("runners.serverCertLabel")}</label>
          <textarea
            id="runner-server-ca"
            rows={4}
            value={serverCA}
            placeholder="-----BEGIN CERTIFICATE-----"
            onChange={(e) => setServerCA(e.target.value)}
            className="runner-pem"
          />
          <div className="desc">{t("runners.serverCertDesc")}</div>
        </div>

        <div className="sf-field">
          <label htmlFor="runner-client-cert">{t("runners.clientCertLabel")}</label>
          <textarea
            id="runner-client-cert"
            rows={4}
            value={clientCert}
            placeholder="-----BEGIN CERTIFICATE-----"
            onChange={(e) => setClientCert(e.target.value)}
            className="runner-pem"
          />
          <div className="desc">{t("runners.clientCertDesc")}</div>
        </div>

        <div className="sf-field">
          <label htmlFor="runner-client-key">{t("runners.clientKeyLabel")}</label>
          <textarea
            id="runner-client-key"
            rows={4}
            value={clientKey}
            autoComplete="off"
            placeholder="-----BEGIN PRIVATE KEY-----"
            onChange={(e) => setClientKey(e.target.value)}
            className="runner-pem"
          />
          <div className="desc">{t("runners.clientKeyDesc")}</div>
        </div>

        {probe && <ProbeResult probe={probe} />}

        <div className="runner-form-actions">
          <Button onClick={() => void test()} disabled={!complete || probing}>
            {probing ? t("runners.testing") : t("runners.test")}
          </Button>
          <Button variant="primary" onClick={() => void save()} disabled={!complete || saving}>
            {saving ? t("common.saving") : t("runners.save")}
          </Button>
        </div>
      </div>
    </div>
  );
}

// RunnerStateChip reuses the status-chip vocabulary the runs surfaces use, so
// "connected" reads the same way "succeeded" does elsewhere.
function RunnerStateChip({ runner }: { runner: Runner }) {
  const { t } = useTranslation();
  const state = runner.state ?? "unreachable";
  const tone =
    state === "connected" ? "succeeded" : state === "disabled" ? "queued" : "failed";
  return (
    <span className={"status-chip " + tone}>
      <span className={"status-dot " + tone} />
      {t(`runners.state.${state}`)}
      {state === "connected" && runner.last_success
        ? ` · ${formatDateTime(runner.last_success)}`
        : ""}
    </span>
  );
}

// ProbeResult is the Test outcome.
//
// It leads with the certificate subject rather than a tick: an address
// answering only says something is listening, while the subject and the list of
// steps are how an admin confirms the thing on the other end is theirs.
function ProbeResult({ probe }: { probe: RunnerProbe }) {
  const { t } = useTranslation();
  if (!probe.ok) {
    return (
      <ErrorNotice>
        {probe.subject ? (
          <>
            <strong>{probe.subject}</strong>
            <div className="desc">{probe.error}</div>
          </>
        ) : (
          probe.error
        )}
      </ErrorNotice>
    );
  }
  return (
    <Notice className="runner-probe">
      <strong>{probe.subject || t("runners.probeAnswered")}</strong>
      <div className="desc">
        {probe.drops?.length
          ? t("runners.probeSteps", { steps: probe.drops.join(" · ") })
          : t("runners.probeNoSteps")}
      </div>
    </Notice>
  );
}
