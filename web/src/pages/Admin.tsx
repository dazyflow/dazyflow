import { KeyRound, Users, Settings2, Boxes, ShieldAlert } from "lucide-react";
import { useAuth } from "../auth";

// Admin is the gating point for tenant-level configuration. The
// concrete management UIs (API keys, users, workspace settings) are
// stubs today — they need matching backend endpoints (see TODO).
export function Admin() {
  const { me, hasPerm } = useAuth();
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
            Tenant <strong>{me?.tenant}</strong> · workspace{" "}
            <strong>{me?.workspace}</strong>
          </div>
        </div>
      </div>

      <div className="admin-grid">
        <AdminCard
          icon={<KeyRound size={16} />}
          title="API keys"
          desc="Issue, list, and revoke API keys. UI stub — wires up once the daemon exposes /api/v1/admin/api-keys."
          status="stub"
        />
        <AdminCard
          icon={<Users size={16} />}
          title="Users & roles"
          desc="Map principals to roles. The role system already exists in core; the UI needs a roles-CRUD endpoint."
          status="stub"
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
          desc="View graph saves, runs, secret accesses, approval decisions."
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
}: {
  icon: React.ReactNode;
  title: string;
  desc: string;
  status: "stub" | "ready";
}) {
  return (
    <div className="admin-card">
      <h3>
        {icon}
        {title}
      </h3>
      <div className="desc">{desc}</div>
      <span className="badge">{status === "stub" ? "Stub" : "Ready"}</span>
    </div>
  );
}
