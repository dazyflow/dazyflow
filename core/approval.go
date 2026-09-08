// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

// Off by default, and that default is the point: the authenticated endpoint
// accepts any key holding workspace membership, so a key minted to run a flow
// could rubber-stamp the gate put there to stop it.
//
// It cannot govern the signed approval link: nothing about an inbound POST
// distinguishes the human who clicked from a script holding the same URL.
func ApprovalAllowsAPI(params map[string]any) bool {
	b, _ := params["allow_api"].(bool)
	return b
}

func ApprovalStepRequiresHuman(g Graph, nodeID string) bool {
	for _, n := range g.Nodes {
		if n.ID == nodeID && n.Module == ApprovalModuleID {
			return !ApprovalAllowsAPI(n.Params)
		}
	}
	return false
}
