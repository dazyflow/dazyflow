import { Link } from "react-router-dom";
import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { useAuth } from "../auth";

// Admin is the gating point for tenant-level configuration. Each card
// links to a focused sub-page when the underlying API + UI exists, and
// stays as a stub otherwise. The role gate accepts either tenant:admin
// (the right one) or graph:admin (a coarser fallback so power users
// who set the system up can land here even before refining roles).
export function Admin() {
  const { me, hasPerm, activeTenant, activeWorkspace } = useAuth();
  if (!hasPerm("tenant:admin") && !hasPerm("graph:admin")) {
    return (
      <div className="card" style={{ color: "var(--danger)" }}>
        You need <code>tenant:admin</code> or <code>graph:admin</code> to
        view this page.
      </div>
    );
  }
  return (
    <div>
      <div className="page-title">
        <div>
          <h1>Admin</h1>
          <div className="sub">
            Tenant <strong>{activeTenant || me?.tenant}</strong> · workspace{" "}
            <strong>{activeWorkspace || me?.workspace || "(any)"}</strong>
          </div>
        </div>
      </div>

      <div className="admin-grid">
        <AdminCard
          to="/admin/api-keys"
          icon={<KeyRound size={16} />}
          title="API keys"
          desc="Issue, list, and revoke bearer tokens for this tenant."
          status="ready"
        />
        <AdminCard
          to="/admin/users"
          icon={<Users size={16} />}
          title="Users & roles"
          desc="Subjects derived from API keys, grouped with their effective permissions."
          status="ready"
        />
        <AdminCard
          icon={<Settings2 size={16} />}
          title="Workspace settings"
          desc="Quotas, sandbox roots, retention. Reads/writes to the daemon's tenant config."
          status="stub"
        />
        <AdminCard
          icon={<Boxes size={16} />}
          title="Module registry"
          desc="Inspect installed modules and (later) approve remote/MCP modules."
          status="stub"
        />
        <AdminCard
          icon={<ShieldAlert size={16} />}
          title="Audit log"
          desc="Graph saves, runs, secret accesses, approval decisions — needs persistence + instrumentation."
          status="stub"
        />
      </div>
    </div>
  );
}

function AdminCard({
  icon,
  title,
  desc,
  status,
  to,
}: {
  icon: React.ReactNode;
  title: string;
  desc: string;
  status: "stub" | "ready";
  to?: string;
}) {
  const body = (
    <div className="admin-card">
      <h3>
        {icon}
        {title}
      </h3>
      <div className="desc">{desc}</div>
      <span className="badge">{status === "stub" ? "Stub" : "Ready"}</span>
    </div>
  );
  return to ? (
    <Link to={to} style={{ textDecoration: "none", color: "inherit" }}>
      {body}
    </Link>
  ) : (
    body
  );
}
