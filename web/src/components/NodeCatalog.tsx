import { useMemo, useState, type DragEvent } from "react";
import { Search } from "lucide-react";
import { iconFor } from "../icons";
import type { Manifest } from "../types";

type Props = {
  modules: Manifest[];
};

// Mapping from category keys to display labels — keeps the UI in one
// place even as categories evolve on the backend.
const categoryLabel: Record<string, string> = {
  trigger: "Triggers",
  flow_control: "Flow control",
  network: "Network",
  io: "I/O",
  ai: "AI",
  transformation: "Transform",
  external: "External",
  system: "System",
};

export function NodeCatalog({ modules }: Props) {
  const [query, setQuery] = useState("");
  const filtered = useMemo(() => {
    const q = query.toLowerCase().trim();
    if (!q) return modules;
    return modules.filter(
      (m) =>
        m.id.toLowerCase().includes(q) ||
        m.label.toLowerCase().includes(q) ||
        (m.description ?? "").toLowerCase().includes(q) ||
        (m.tags ?? []).some((t) => t.toLowerCase().includes(q)),
    );
  }, [modules, query]);

  // Group by category for visual organization.
  const groups = useMemo(() => {
    const map = new Map<string, Manifest[]>();
    for (const m of filtered) {
      const k = m.category ?? "other";
      const arr = map.get(k) ?? [];
      arr.push(m);
      map.set(k, arr);
    }
    return Array.from(map.entries()).sort(([a], [b]) =>
      a.localeCompare(b),
    );
  }, [filtered]);

  const onDragStart = (e: DragEvent<HTMLDivElement>, m: Manifest) => {
    e.dataTransfer.setData("application/x-hazyflow-module", m.id);
    e.dataTransfer.effectAllowed = "copy";
  };

  return (
    <>
      <div className="panel-head">
        <span>Nodes</span>
        <span style={{ color: "var(--faint)", fontSize: 11 }}>
          {modules.length}
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
            placeholder="Search modules…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            style={{ paddingLeft: 30 }}
          />
        </div>
      </div>
      <div className="catalog-list">
        {groups.map(([cat, mods]) => (
          <div key={cat} className="catalog-group">
            <h3>{categoryLabel[cat] ?? cat}</h3>
            <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
              {mods.map((m) => {
                const Icon = iconFor(m.icon, m.category);
                const color = m.color ?? "#9f83fe";
                return (
                  <div
                    key={m.id}
                    className="module-row"
                    draggable
                    onDragStart={(e) => onDragStart(e, m)}
                    title={m.description ?? m.label}
                  >
                    <div
                      className="icon"
                      style={{
                        background: `linear-gradient(135deg, ${color}, color-mix(in srgb, ${color} 70%, #fff))`,
                      }}
                    >
                      <Icon size={16} color="#140d30" strokeWidth={2.2} />
                    </div>
                    <div style={{ minWidth: 0 }}>
                      <div className="name">{m.label}</div>
                      <div className="meta">{m.id}</div>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        ))}
      </div>
    </>
  );
}
