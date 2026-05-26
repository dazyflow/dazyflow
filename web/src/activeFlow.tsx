import { createContext, useContext } from "react";

// ActiveFlow lets a page deep in the tree (the editor) publish the
// currently-open flow's display name up to the AppShell top bar, which
// renders it in place of the "Hazy Flow" wordmark. Kept as a tiny
// context rather than a second graph fetch in AppShell so there's one
// source of truth (the editor already loaded the graph) and no extra
// round trip.
type ActiveFlow = {
  name: string | null;
  setName: (name: string | null) => void;
};

export const ActiveFlowContext = createContext<ActiveFlow>({
  name: null,
  setName: () => {},
});

export const useActiveFlow = () => useContext(ActiveFlowContext);
