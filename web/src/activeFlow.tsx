// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
  icon: string | null;
  setIcon: (icon: string | null) => void;
  openSettings: (() => void) | null;
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

export const FLOWS_CHANGED_EVENT = "dazyflow:flows-changed";
