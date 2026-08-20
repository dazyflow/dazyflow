// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

/**
 * Identity-preserving reconciliation for React Flow's node/edge arrays.
 *
 * WHY THIS EXISTS
 *
 * hydrateGraph rebuilds every node and edge object from scratch. That is fine
 * when loading a flow — everything is new anyway — but it makes applying a
 * snapshot expensive: displayNodes memoises each card on the node object BY
 * REFERENCE (its deps array starts with `n`), so fresh objects miss the cache
 * and every card on the canvas rebuilds its data and re-renders. On a large
 * flow an undo would visibly flash the whole canvas.
 *
 * Reconciling instead keeps the existing object for every item whose content
 * is unchanged, so applying a snapshot re-renders only what genuinely differs.
 * Undoing a single node's drag touches one card, not fifty.
 *
 * The functions are generic and pure so they can be unit-tested without React
 * Flow or a DOM, and so the same reconciliation serves the other paths that
 * currently rebuild wholesale — the MCP live-watch apply and history preview.
 */

export interface ReconcileResult<T> {
  items: T[];
  /** How many existing objects were reused by reference. Asserted in tests. */
  reused: number;
  /** How many were built fresh (added or changed). */
  built: number;
}

/**
 * reconcileByID produces the target list while reusing existing objects
 * wherever `isUnchanged` says the content matches.
 *
 * Order follows `targets`, because React Flow renders (and z-orders) in array
 * order. When every item is reused AND the order is unchanged, the ORIGINAL
 * array is returned by reference — so a no-op apply doesn't even re-render the
 * canvas.
 */
export function reconcileByID<T, D>(
  existing: readonly T[],
  targets: readonly D[],
  opts: {
    idOfExisting: (item: T) => string;
    idOfTarget: (target: D) => string;
    isUnchanged: (item: T, target: D) => boolean;
    build: (target: D, previous: T | undefined) => T;
  },
): ReconcileResult<T> {
  const byID = new Map<string, T>();
  for (const item of existing) byID.set(opts.idOfExisting(item), item);

  let reused = 0;
  let built = 0;
  let orderIntact = existing.length === targets.length;

  const items = targets.map((target, i) => {
    const prev = byID.get(opts.idOfTarget(target));
    if (prev !== undefined && opts.isUnchanged(prev, target)) {
      reused++;
      if (orderIntact && existing[i] !== prev) orderIntact = false;
      return prev;
    }
    built++;
    orderIntact = false;
    return opts.build(target, prev);
  });

  if (built === 0 && orderIntact) {
    return { items: existing as T[], reused, built };
  }
  return { items, reused, built };
}

/**
 * samePosition compares two coordinates with a tolerance.
 *
 * React Flow writes fractional positions during a drag, and a snapshot
 * round-trips through JSON, so exact equality would report a node as moved
 * because of a value the user cannot see or have caused. Half a pixel is below
 * the threshold of a rendered difference.
 */
export function samePosition(
  a: { x: number; y: number } | undefined,
  b: { x: number; y: number } | undefined,
): boolean {
  if (!a || !b) return a === b;
  return Math.abs(a.x - b.x) < 0.5 && Math.abs(a.y - b.y) < 0.5;
}

/** Structural equality for the small plain-data bits carried on nodes/edges. */
export function sameData(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  return JSON.stringify(a ?? null) === JSON.stringify(b ?? null);
}
