// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// The approval step's authorization surface. ApprovalModuleID itself lives in
// lint.go, where the graph checks that reference it are.

// ApprovalAllowsAPI reports whether an await_approval node may be decided by
// an API key rather than a person.
//
// Off by default, and that default is the point. A pause on this step means
// "a human looks at this before the flow continues", and the authenticated
// approval endpoint accepts any key holding workspace membership — so a key
// minted to run a flow or read its runs could also rubber-stamp the gate that
// was put there to stop it. Requiring the step to say out loud that a machine
// may decide it keeps a key's blast radius smaller than its membership.
//
// This governs the API-key path, where the credential says whether the caller
// is a person. It cannot govern the signed approval link: that is a URL sent
// to a human, and nothing about an inbound POST distinguishes the human who
// clicked from a script holding the same link. A step that must never be
// machine-decided should therefore not have its link piped anywhere a script
// can read it.
func ApprovalAllowsAPI(params map[string]any) bool {
	b, _ := params["allow_api"].(bool)
	return b
}

// ApprovalStepRequiresHuman reports whether nodeID is an await_approval step
// that has NOT opted into machine decisions — the one case an API key must be
// turned away from.
//
// Any other node answers false, including one the graph does not contain. The
// gate is about approval steps, and answering "this approval step is for a
// person" about a node that is not an approval step at all would be a
// misleading refusal in place of the accurate "that node is not awaiting".
// Nothing is opened by that: a nodeID absent from the run's own pinned graph
// has no awaiting record either, so the normal path still refuses it.
func ApprovalStepRequiresHuman(g Graph, nodeID string) bool {
	for _, n := range g.Nodes {
		if n.ID == nodeID && n.Module == ApprovalModuleID {
			return !ApprovalAllowsAPI(n.Params)
		}
	}
	return false
}
