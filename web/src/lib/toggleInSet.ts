// Returns a copy of `set` with `item` added if absent or removed if present.
// Never mutates the input, so it is safe inside a React state updater.
export function toggleInSet<T>(set: ReadonlySet<T>, item: T): Set<T> {
  const next = new Set(set);
  if (next.has(item)) next.delete(item);
  else next.add(item);
  return next;
}
