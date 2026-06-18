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
  // icon: the open flow's icon (data: URL / name), shown next to the
  // name in the top bar when set. Null when the flow uses the default.
  icon: string | null;
  setIcon: (icon: string | null) => void;
  openSettings: (() => void) | null;
  // Dispatch type (not a plain setter) so the editor can use the
  // functional-update form — `setOpenSettings(() => myFn)` — to store a
  // callback in state without React mistaking it for an updater.
  setOpenSettings: Dispatch<SetStateAction<(() => void) | null>>;
};

export const ActiveFlowContext = createContext<ActiveFlow>({
  name: null,
  setName: () => {},
  icon: null,
  setIcon: () => {},
  openSettings: null,
  setOpenSettings: () => {},
});

export const useActiveFlow = () => useContext(ActiveFlowContext);

// FLOWS_CHANGED_EVENT fires on window after a flow's name/icon/visibility
// is persisted, so the sidebar flow list refetches and reflects the
// change without needing a navigation.
export const FLOWS_CHANGED_EVENT = "dazyflow:flows-changed";
