// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

import { BaseEdge, getBezierPath, useReactFlow, type EdgeProps } from "@xyflow/react";
import {
  useCallback,
  type CSSProperties,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import i18n from "../i18n";

// RerouteEdge draws a connection through optional "knot" waypoints — the
// approach-B reroute (see #4): the edge stays a single source→target
// connection, and the bend points live as UI-only metadata on the edge
// (edge.data.waypoints, in flow coordinates). The engine never sees them;
// they exist purely to let the user route a wire cleanly around nodes.
//
// Interactions: double-click the wire to drop a knot at that point (into
// the nearest segment), drag a knot to move it, double-click a knot to
// remove it. All mutations go through data.updateWaypoints, supplied by
// FlowEditor so the change lands in the controlled edge state (and marks
// the graph dirty).

type WP = { x: number; y: number };
type RerouteData = {
  waypoints?: WP[];
  updateWaypoints?: (wps: WP[]) => void;
  // True while data is flowing along this wire during a run (#9) — drives
  // the travelling-dot animation.
  active?: boolean;
  // Set transiently while a freshly-built or externally-edited graph
  // animates onto the canvas: the wire "draws in" (stroke-dashoffset sweep)
  // after drawDelay seconds, so connections appear to be made after the
  // drops they join settle. Cleared once the animation window passes. See
  // applyGraphAnimated in FlowEditor.
  drawDelay?: number;
};

// splinePath builds a smooth cubic curve passing through every point via
// Catmull-Rom → bezier conversion, so the wire bends through each knot
// instead of kinking at it. Endpoints clamp (phantom = first/last point)
// so the curve starts/ends cleanly.
function splinePath(pts: WP[]): string {
  if (pts.length < 2) return "";
  let d = `M ${pts[0].x},${pts[0].y}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] ?? pts[i];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[i + 2] ?? pts[i + 1];
    const cp1x = p1.x + (p2.x - p0.x) / 6;
    const cp1y = p1.y + (p2.y - p0.y) / 6;
    const cp2x = p2.x - (p3.x - p1.x) / 6;
    const cp2y = p2.y - (p3.y - p1.y) / 6;
    d += ` C ${cp1x},${cp1y} ${cp2x},${cp2y} ${p2.x},${p2.y}`;
  }
  return d;
}

// Squared distance from point p to segment a–b. Squared is enough for
// "which segment is nearest" and avoids the sqrt.
function distToSeg(p: WP, a: WP, b: WP): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const len2 = dx * dx + dy * dy || 1;
  let t = ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2;
  t = Math.max(0, Math.min(1, t));
  const cx = a.x + t * dx;
  const cy = a.y + t * dy;
  return (p.x - cx) ** 2 + (p.y - cy) ** 2;
}

export function RerouteEdge({
  id,
  source,
  target,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style,
  markerEnd,
  data,
  selected,
}: EdgeProps) {
  const { screenToFlowPosition } = useReactFlow();
  const d = (data ?? {}) as RerouteData;
  const waypoints = d.waypoints ?? [];
  const update = d.updateWaypoints;

  const pts: WP[] = [
    { x: sourceX, y: sourceY },
    ...waypoints,
    { x: targetX, y: targetY },
  ];
  // With no knots, use React Flow's bezier so a plain wire looks exactly
  // like the default curved edge (leaving the ports horizontally). With
  // knots, a Catmull-Rom spline curves smoothly through each one.
  const [bezier] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  });
  const path = waypoints.length === 0 ? bezier : splinePath(pts);
  const stroke = (style?.stroke as string) ?? "var(--accent)";

  const onAddKnot = useCallback(
    (e: ReactMouseEvent) => {
      if (!update) return;
      e.stopPropagation();
      const pos = screenToFlowPosition({ x: e.clientX, y: e.clientY });
      let best = 0;
      let bestD = Infinity;
      for (let i = 0; i < pts.length - 1; i++) {
        const dd = distToSeg(pos, pts[i], pts[i + 1]);
        if (dd < bestD) {
          bestD = dd;
          best = i;
        }
      }
      const next = [...waypoints];
      next.splice(best, 0, pos);
      update(next);
    },
    [update, screenToFlowPosition, pts, waypoints],
  );

  const onKnotDown = useCallback(
    (i: number) => (e: ReactPointerEvent) => {
      if (!update) return;
      e.stopPropagation();
      e.preventDefault();
      const move = (ev: PointerEvent) => {
        const pos = screenToFlowPosition({ x: ev.clientX, y: ev.clientY });
        update(waypoints.map((w, j) => (j === i ? pos : w)));
      };
      const up = () => {
        window.removeEventListener("pointermove", move);
        window.removeEventListener("pointerup", up);
      };
      window.addEventListener("pointermove", move);
      window.addEventListener("pointerup", up);
    },
    [update, screenToFlowPosition, waypoints],
  );

  const onKnotRemove = (i: number) => (e: ReactMouseEvent) => {
    if (!update) return;
    e.stopPropagation();
    update(waypoints.filter((_, j) => j !== i));
  };

  const drawing = typeof d.drawDelay === "number";
  return (
    // Label the connection for assistive tech: which two steps it wires.
    <g role="img" aria-label={i18n.t("rerouteEdge.connectionLabel", { source, target })}>
      {drawing ? (
        // Draw-in: pathLength=1 normalises the dash so the sweep is uniform
        // regardless of wire length; the stroke-dashoffset animation (CSS,
        // keyed off .rf-edge-draw) reveals the wire source→target after the
        // staggered delay. Replaced by the real BaseEdge once the flag clears.
        <path
          className="rf-edge-draw"
          d={path}
          fill="none"
          stroke={stroke}
          strokeWidth={(style?.strokeWidth as number) ?? 2}
          pathLength={1}
          style={{ ["--draw-delay"]: `${d.drawDelay}s` } as CSSProperties}
        />
      ) : (
        <BaseEdge id={id} path={path} style={style} markerEnd={markerEnd} />
      )}
      {/* Data-flow animation (#9): dots travel source→target along the path
          while the downstream node is running. Two staggered dots read as
          continuous flow. animateMotion follows the path's draw direction. */}
      {d.active &&
        [0, 0.6].map((delay, i) => (
          <circle key={i} r={3.5} fill={stroke} className="reroute-flow">
            <animateMotion dur="1.2s" repeatCount="indefinite" path={path} begin={`-${delay}s`} />
          </circle>
        ))}
      {/* Wide, invisible hit area so double-clicking anywhere near the wire
          drops a knot. pointerEvents: stroke keeps clicks on the line. */}
      <path
        d={path}
        fill="none"
        stroke="transparent"
        strokeWidth={14}
        style={{ pointerEvents: "stroke", cursor: "copy" }}
        onDoubleClick={onAddKnot}
      />
      {waypoints.map((w, i) => (
        <circle
          key={i}
          cx={w.x}
          cy={w.y}
          r={selected ? 5 : 4}
          className="reroute-knot"
          fill={stroke}
          onPointerDown={onKnotDown(i)}
          onDoubleClick={onKnotRemove(i)}
        />
      ))}
    </g>
  );
}
