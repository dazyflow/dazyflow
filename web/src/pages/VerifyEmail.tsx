import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { CheckCircle2, MailWarning } from "lucide-react";
import { useTranslation } from "react-i18next";
import { api } from "../api";
import { explainApiError } from "../lib/explainApiError";
import { useAuth } from "../auth";

// VerifyEmail is the landing page for the confirmation link
// (/verify-email?email=…&token=…). It works signed-in or not — the
// token in the link is the proof, not the session — so it's routed in
// both App trees. On success it refreshes whoami so the pending banner
// disappears without a reload.
export function VerifyEmail() {
  const { t } = useTranslation();
  const { token: sessionToken, refreshMe } = useAuth();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [state, setState] = useState<"working" | "ok" | "failed">("working");
  const [errMsg, setErrMsg] = useState("");

  const email = params.get("email") ?? "";
  const verifyToken = params.get("token") ?? "";

  useEffect(() => {
    if (!email || !verifyToken) {
      setState("failed");
      setErrMsg(t("verifyEmail.badLink"));
      return;
    }
    api
      .verifyEmail(email, verifyToken)
      .then(async () => {
        setState("ok");
        if (sessionToken) {
          // Refresh whoami so the pending banner clears, then return the
          // user to the welcome flow they came from to keep onboarding.
          await refreshMe();
          navigate("/welcome", { replace: true });
        }
      })
      .catch((e) => {
        setState("failed");
        setErrMsg(explainApiError(e, t));
      });
    // Run once for the link in the URL — re-running on auth changes
    // would double-post the token.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="auth-page" style={{ display: "grid", placeItems: "center", minHeight: "60vh" }}>
      <div className="card" style={{ maxWidth: 440, textAlign: "center", padding: "var(--space-6)" }}>
        {state === "working" && <p>{t("verifyEmail.working")}</p>}
        {state === "ok" && (
          <>
            <CheckCircle2 size={36} style={{ color: "var(--success, #22c55e)" }} />
            <h1 style={{ marginTop: "var(--space-3)" }}>{t("verifyEmail.okTitle")}</h1>
            <p className="sub">{t("verifyEmail.okBody")}</p>
            <p>
              <Link to={sessionToken ? "/welcome" : "/signin"} className="primary-link">
                {sessionToken ? t("verifyEmail.toApp") : t("verifyEmail.toSignin")}
              </Link>
            </p>
          </>
        )}
        {state === "failed" && (
          <>
            <MailWarning size={36} style={{ color: "var(--danger)" }} />
            <h1 style={{ marginTop: "var(--space-3)" }}>{t("verifyEmail.failedTitle")}</h1>
            <p className="sub">{errMsg}</p>
            <p className="sub">{t("verifyEmail.failedHint")}</p>
            {/* The failed state used to dead-end here: the hint says "sign in
                and resend" but offered no way to get there. A user arriving
                from an expired email link is signed out, so give them the
                clickable path. */}
            <p>
              <Link
                to={sessionToken ? "/welcome" : "/signin"}
                className="primary-link"
              >
                {sessionToken ? t("verifyEmail.toApp") : t("verifyEmail.toSignin")}
              </Link>
            </p>
          </>
        )}
      </div>
    </div>
  );
}
