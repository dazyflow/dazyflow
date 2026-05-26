import { createContext, useContext, type Dispatch, type SetStateAction } from "react";

// ActiveFlow bridges the open flow (the editor, deep in the tree) and
// the AppShell chrome. The editor publishes:
//   - name:        shown in the top-bar wordmark slot
//   - openSettings: a callback the top-right "current flow" three-dots
//                   menu invokes to open the flow-settings modal, which
//                   lives inside the editor component.
// Kept as a tiny context (not a second graph fetch / lifted modal) so
// there's one source of truth and the editor keeps owning its modal.
type ActiveFlow = {
  name: string | null;
  setName: (name: string | null) => void;
  openSettings: (() => void) | null;
  // Dispatch type (not a plain setter) so the editor can use the
  // functional-update form — `setOpenSettings(() => myFn)` — to store a
  // callback in state without React mistaking it for an updater.
  setOpenSettings: Dispatch<SetStateAction<(() => void) | null>>;
};

export const ActiveFlowContext = createContext<ActiveFlow>({
  name: null,
  setName: () => {},
  openSettings: null,
  setOpenSettings: () => {},
});

export const useActiveFlow = () => useContext(ActiveFlowContext);
