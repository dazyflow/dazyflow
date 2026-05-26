import { Link } from "react-router-dom";
import { useAuth } from "../auth";

// Welcome is the post-signup landing wizard — the "first-run"
// surface from the T0-3 TODO. Intentionally simple: three CTAs that
// point at the highest-leverage next actions, plus a confirmation
// of the tenant the user just got. The full step-by-step walkthrough
// (templates gallery, guided node-drop tutorial) becomes useful once
// templates ship; for now this is the right surface for "you're in,
// here's what you can do."
export function Welcome() {
  const { me } = useAuth();
  return (
    <div className="welcome">
      <div className="card welcome-card">
        <h1>Welcome to Hazy Flow</h1>
        {me?.subject && (
          <p className="welcome-sub">
            Signed in as <strong>{me.subject}</strong>
            {me.tenant && (
              <>
                {" "}
                in tenant <code>{me.tenant}</code>
              </>
            )}
            .
          </p>
        )}
        <p>
          You're ready to build. Three ways to start:
        </p>
        <ol className="welcome-steps">
          <li>
            <h2>1. Start from a template</h2>
            <p>
              Pre-built workflows you can fork in one click — Excel
              → DB, webhook → Slack, daily reports, and more.
              Fastest way to see what's possible.
            </p>
            <Link to="/templates" className="primary welcome-cta">
              Browse templates
            </Link>
          </li>
          <li>
            <h2>2. Build from scratch</h2>
            <p>
              Drag nodes from the catalog onto a blank canvas and
              wire them up. Connect Slack / Gmail / Sheets / Postgres
              and the rest from the integrations catalog.
            </p>
            <Link to="/flows" className="welcome-cta">
              Open editor
            </Link>
          </li>
          <li>
            <h2>3. Skim a recent run</h2>
            <p>
              When workflows fire (on a schedule, a webhook, or
              manually), every step's input and output is captured
              here for debugging.
            </p>
            <Link to="/runs" className="welcome-cta">
              See runs
            </Link>
          </li>
        </ol>
        <p className="welcome-foot">
          You can come back to this page any time at{" "}
          <Link to="/welcome">/welcome</Link>.
        </p>
      </div>
    </div>
  );
}
