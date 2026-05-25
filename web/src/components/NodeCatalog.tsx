import { useEffect, useMemo, useState, type DragEvent } from "react";
import { ChevronDown, ChevronRight, Search, Box } from "lucide-react";
import { iconFor, isBrandedIcon } from "../icons";
import type { Manifest } from "../types";

type Props = {
  drops: Manifest[];
};

// Display label for the standard-library fallback group (drops without
// an Integration). Goes at the bottom and starts collapsed.
const STDLIB_KEY = "__stdlib__";
const STDLIB_LABEL = "Standard library";

// Map category keys → friendly labels for the standard-library section's
// sub-headings.
const categoryLabel: Record<string, string> = {
  trigger: "Triggers",
  flow_control: "Flow control",
  network: "Network",
  io: "Files & I/O",
  ai: "AI",
  transformation: "Transform",
  external: "External",
  system: "System",
};

// integrationIcon maps an Integration display name to one of the icons
// in iconRegistry. Falls back to a generic Box when unknown.
function integrationIcon(name: string): string | undefined {
  const k = name.toLowerCase();
  if (k === "git") return "git";
  if (k === "ntfy") return "ntfy";
  if (k === "claude" || k === "anthropic") return "claude";
  if (k === "email") return "mail";
  if (k === "http") return "globe";
  return undefined;
}

const COLLAPSE_STORAGE_KEY = "hazyflow.catalog.collapsed";

function loadCollapsed(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(COLLAPSE_STORAGE_KEY);
    return raw ? JSON.parse(raw) : { [STDLIB_KEY]: true };
  } catch {
    return { [STDLIB_KEY]: true };
  }
}

// stripPrefix removes a leading "<Integration> " from a drop's label so
// rows under "Git" read as "Checkout" / "Build" instead of "Git
// checkout". Case-insensitive prefix match.
function stripPrefix(label: string, integration: string): string {
  const p = integration + " ";
  if (label.toLowerCase().startsWith(p.toLowerCase())) {
    const rest = label.slice(p.length);
    return rest.charAt(0).toUpperCase() + rest.slice(1);
  }
  return label;
}

export function NodeCatalog({ drops }: Props) {
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] =
    useState<Record<string, boolean>>(loadCollapsed);

  useEffect(() => {
    localStorage.setItem(COLLAPSE_STORAGE_KEY, JSON.stringify(collapsed));
  }, [collapsed]);

  const toggle = (key: string) =>
    setCollapsed((prev) => ({ ...prev, [key]: !prev[key] }));

  const filtered = useMemo(() => {
    const q = query.toLowerCase().trim();
    if (!q) return drops;
    return drops.filter(
      (m) =>
        m.id.toLowerCase().includes(q) ||
        m.label.toLowerCase().includes(q) ||
        (m.integration ?? "").toLowerCase().includes(q) ||
        (m.description ?? "").toLowerCase().includes(q) ||
        (m.tags ?? []).some((t) => t.toLowerCase().includes(q)),
    );
  }, [drops, query]);

  // Group by Integration when set; everything else lands in a single
  // "Standard library" bucket pinned to the bottom.
  const sections = useMemo(() => {
    const integrations = new Map<string, Manifest[]>();
    const stdlib: Manifest[] = [];
    for (const m of filtered) {
      if (m.integration) {
        const arr = integrations.get(m.integration) ?? [];
        arr.push(m);
        integrations.set(m.integration, arr);
      } else {
        stdlib.push(m);
      }
    }
    const ordered: Array<{
      key: string;
      label: string;
      icon?: string;
      drops: Manifest[];
      isStdlib: boolean;
    }> = Array.from(integrations.entries())
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, drops]) => ({
        key: name,
        label: name,
        icon: integrationIcon(name),
        drops,
        isStdlib: false,
      }));
    if (stdlib.length > 0) {
      ordered.push({
        key: STDLIB_KEY,
        label: STDLIB_LABEL,
        drops: stdlib,
        isStdlib: true,
      });
    }
    return ordered;
  }, [filtered]);

  const onDragStart = (e: DragEvent<HTMLDivElement>, m: Manifest) => {
    e.dataTransfer.setData("application/x-hazyflow-module", m.id);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <>
      <div className="panel-head">
        <span>Integrations</span>
        <span style={{ color: "var(--faint)", fontSize: 11 }}>
          {drops.length}
        </span>
      </div>
      <div className="catalog-search">
        <div style={{ position: "relative" }}>
          <Search
            size={14}
            style={{
              position: "absolute",
              left: 10,
              top: "50%",
              transform: "translateY(-50%)",
              color: "var(--muted)",
            }}
          />
          <input
            placeholder="Search integrations…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            style={{ paddingLeft: 30 }}
          />
        </div>
      </div>
      <div className="catalog-list">
        {sections.map((s) => {
          // An active search auto-expands every section so matches
          // don't hide behind a collapsed header.
          const isCollapsed = !query && !!collapsed[s.key];
          const HeaderIcon = s.icon ? iconFor(s.icon) : Box;
          const headerBranded = isBrandedIcon(s.icon);
          return (
            <div key={s.key} className="catalog-group">
              <button
                type="button"
                className="catalog-group-header"
                onClick={() => toggle(s.key)}
                aria-expanded={!isCollapsed}
              >
                {isCollapsed ? (
                  <ChevronRight size={12} />
                ) : (
                  <ChevronDown size={12} />
                )}
                {!s.isStdlib && (
                  <span className="catalog-integration-icon">
                    <HeaderIcon
                      size={headerBranded ? 18 : 14}
                      color={headerBranded ? undefined : "currentColor"}
                      strokeWidth={2}
                    />
                  </span>
                )}
                <span className="catalog-group-label">{s.label}</span>
                <span className="catalog-group-count">{s.drops.length}</span>
              </button>
              {!isCollapsed && (
                <div className="catalog-group-body">
                  {s.isStdlib
                    ? renderStdlib(s.drops, onDragStart)
                    : renderDrops(s.label, s.drops, onDragStart)}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </>
  );
}

// renderDrops renders the drops inside an Integration section. The label
// strips the integration prefix for shorter reading.
function renderDrops(
  integration: string,
  drops: Manifest[],
  onDragStart: (e: DragEvent<HTMLDivElement>, m: Manifest) => void,
) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      {drops.map((m) => (
        <DropRow
          key={m.id}
          drop={m}
          shortLabel={stripPrefix(m.label, integration)}
          onDragStart={onDragStart}
        />
      ))}
    </div>
  );
}

// renderStdlib renders the standard-library section, sub-grouped by
// category so flow-control / files / triggers each get their own
// labelled run inside.
function renderStdlib(
  drops: Manifest[],
  onDragStart: (e: DragEvent<HTMLDivElement>, m: Manifest) => void,
) {
  const byCat = new Map<string, Manifest[]>();
  for (const m of drops) {
    const k = m.category ?? "other";
    const arr = byCat.get(k) ?? [];
    arr.push(m);
    byCat.set(k, arr);
  }
  const cats = Array.from(byCat.entries()).sort(([a], [b]) =>
    (categoryLabel[a] ?? a).localeCompare(categoryLabel[b] ?? b),
  );
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      {cats.map(([cat, items]) => (
        <div key={cat}>
          <div className="catalog-subhead">{categoryLabel[cat] ?? cat}</div>
          <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            {items.map((m) => (
              <DropRow
                key={m.id}
                drop={m}
                shortLabel={m.label}
                onDragStart={onDragStart}
              />
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function DropRow({
  drop,
  shortLabel,
  onDragStart,
}: {
  drop: Manifest;
  shortLabel: string;
  onDragStart: (e: DragEvent<HTMLDivElement>, m: Manifest) => void;
}) {
  const Icon = iconFor(drop.icon, drop.category);
  const color = drop.color ?? "#9f83fe";
  const branded = isBrandedIcon(drop.icon);
  return (
    <div
      className="module-row"
      draggable
      onDragStart={(e) => onDragStart(e, drop)}
      title={drop.description ?? drop.label}
    >
      {branded ? (
        <div className="icon branded">
          <Icon size={24} strokeWidth={2.2} />
        </div>
      ) : (
        <div
          className="icon"
          style={{
            background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
          }}
        >
          <Icon size={16} color="#140d30" strokeWidth={2.2} />
        </div>
      )}
      <div style={{ minWidth: 0, flex: 1 }}>
        <div className="name">{shortLabel}</div>
        <div className="meta">{drop.id}</div>
      </div>
      {drop.category && (
        <span className="cat-pill" title={`category: ${drop.category}`}>
          {drop.category}
        </span>
      )}
    </div>
  );
}
