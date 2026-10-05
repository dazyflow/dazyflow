// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"strings"
)

// Authoring by conversation, in three layers, the way a person would brief a
// colleague:
//
//   - Instructions, sent in initialize and so always in the model's context:
//     how to lead someone — often on their phone, often by voice — from an
//     idea to a running flow, a step at a time.
//   - get_authoring_guide: the reference, read once before the first edit —
//     the edit ops, wiring, placeholders, and a worked example.
//   - edit_flow: small named changes, one save per turn, which an open canvas
//     shows as they land.

// Instructions is what initialize returns as the server's instructions.
const Instructions = `Dazyflow automates work: a flow is steps wired on a canvas — a trigger that starts it, then steps that read, decide and act in other apps.

You are the user's guide and co-builder. Many are new to this and are on their phone, speaking: keep replies short and easy to listen to — a sentence or two about what you did, never JSON, ids or tool names unless they ask. Read get_authoring_guide once before your first edit.

A new flow goes in five steps. Say the plan in one sentence at the start, and name each step as you reach it.
1. The idea. Ask a few questions at a time, never a form: what should happen, what starts it (a schedule, a form, a webhook, a message), which apps it touches. Offer two or three concrete suggestions with each question and a sensible default for when they don't know.
2. Make it. Create the flow with create_flow holding just its trigger step, then give them the canvas_url from the reply: on their phone or computer it shows the flow, and every change you save appears there within a second.
3. Connect apps. For each app the flow needs, check list_connections; if it is not connected, start_connection and give them the link to sign in. Never put passwords or keys in params: use set_secret and reference it.
4. Build the steps. One edit_flow per change they ask for, with a short note saying what and why — the note is shown live on their canvas. Find steps with list_drops and describe_drop; never invent a step id or a param name. After each edit, say in a sentence what changed and keep count ("that's 3 steps").
5. Try it. Run it with test_trigger_flow or run_flow and wait_for_run, and tell them in plain words what happened; fix with edit_flow. Turn it on (enable_flow, publish_flow) only when they say so.

Throughout:
- End each reply with the next step or a question, so they always know where they are.
- "Undo" means undo_flow_edit with the undo_ref from your last edit_flow.
- Pass base from your last edit_flow reply on the next one. If an edit is refused because the flow changed, someone edited it on the canvas: get_flow, tell them, and redo your change on top.
- An edit that fails changes nothing; read the error, fix the op and send the batch again.`

const authoringGuide = `# Building a flow with edit_flow

## The shape of a flow
Nodes are steps; each has an id (yours to choose, or derived from the step), a module (the step id from list_drops) and params (its settings, schema from describe_drop). Edges wire an output port of one node to an input port of another. A flow starts at a trigger step — webhook_input, form_input, request_input — or on a schedule set with set_flow triggers ([{"type":"cron","cron":"0 8 * * 1-5","tz":"Europe/Stockholm"}]).

## Ops
edit_flow takes ops, applied in order, all or nothing:
- add_node {module, node?, after?, params?, label?} — after wires the new node from that one, using each step's first port: on most steps that is pass, which runs it afterwards and carries the item along.
- update_node {node, params?, label?, disabled?} — params merge; a null value removes that key; replace_params:true replaces them all.
- remove_node {node} — its connections go with it.
- connect {from, to, from_port?, to_port?, on_error?} — to feed a value into a specific input (Slack's text, a sheet's rows), name the ports; describe_drop lists them. Unnamed ports default to each step's first.
- disconnect {from, to, from_port?, to_port?}
- set_flow {name?, description?, triggers?}

New nodes are placed on the canvas next to the node they hang off; nothing the user arranged moves.

## Wiring data
A param can read what earlier steps produced with placeholders: ${upstream.<node>.<port>[0].<field>} for a step's output, ${trigger.body.<field>} for what started the flow, ${secret.<name>} for a stored secret. Shapes are not obvious — call flow_references for the exact paths before writing one.

## Example
"When someone fills in the contact form, post it to Slack":

    [
      {"op":"add_node","node":"contact","module":"form_input","params":{"form_title":"Contact us","form_fields":["name","email","message"]}},
      {"op":"add_node","node":"tell_team","module":"slack_send_message","after":"contact",
       "params":{"account":"default","channel":"#sales","text":"New message from ${trigger.body.name}: ${trigger.body.message}"}},
      {"op":"set_flow","name":"Contact form to Slack"}
    ]

Check the real params with describe_drop before you copy this: it shows the shape, not the settings every step takes.

## After an edit
The reply has report (what was added, changed, removed), nodes (one line per node: "id: module → next"), lint (problems to fix or mention), etag (send it as base next time) and undo_ref (for undo_flow_edit).`

// GuideExampleOps is the worked example in the guide, for tests that hold it
// to the real catalog.
func GuideExampleOps() json.RawMessage {
	start := strings.Index(authoringGuide, "    [\n")
	end := strings.Index(authoringGuide[start:], "    ]\n")
	return json.RawMessage(authoringGuide[start : start+end+len("    ]")])
}

func getAuthoringGuide() Tool {
	return Tool{
		Name:        "get_authoring_guide",
		Description: "The reference for building flows with edit_flow: the ops, wiring, placeholders and a worked example. Read it once before your first edit.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(context.Context, json.RawMessage) (ToolCallResult, error) {
			return ToolCallResult{Content: []ContentItem{{Type: "text", Text: authoringGuide}}}, nil
		},
	}
}

func editFlow(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "edit_flow",
		"Change a flow with a batch of small ops — add, update, remove and connect steps — applied in order and saved once, all or nothing. The way to build a flow turn by turn: an open canvas shows each edit within a second, with your note. See get_authoring_guide for the ops. The reply is compact: report, one line per node, lint, etag (pass as base next time) and undo_ref (for undo_flow_edit).",
		`{
			"type":"object",
			"required":["id","ops"],
			"properties":{
				"id":        {"type":"string","description":"Flow ID."},
				"tenant":    {"type":"string"},
				"workspace": {"type":"string"},
				"note":      {"type":"string","maxLength":200,"description":"One short line on what this edit does, in the user's language — shown live on their canvas (e.g. 'Post new orders to #sales')."},
				"base":      {"type":"string","description":"The etag from your previous edit_flow reply. The edit is refused if the flow changed since, so you never overwrite what the user did on the canvas."},
				"ops": {
					"type":"array","minItems":1,
					"items":{
						"type":"object",
						"required":["op"],
						"properties":{
							"op":             {"type":"string","enum":["add_node","update_node","remove_node","connect","disconnect","set_flow"]},
							"node":           {"type":"string","description":"add_node: the new node's id (optional). update_node / remove_node: the node to change."},
							"module":         {"type":"string","description":"add_node: the step id from list_drops."},
							"after":          {"type":"string","description":"add_node: wire the new node from this node."},
							"label":          {"type":"string"},
							"params":         {"type":"object","description":"Step settings; on update_node they merge, and null removes a key."},
							"replace_params": {"type":"boolean"},
							"disabled":       {"type":"boolean"},
							"from":           {"type":"string"},
							"from_port":      {"type":"string"},
							"to":             {"type":"string"},
							"to_port":        {"type":"string"},
							"on_error":       {"type":"string","enum":["","abort","skip","retry","fallback"]},
							"name":           {"type":"string"},
							"description":    {"type":"string"},
							"triggers":       {"type":"array","items":{"type":"object"},"description":"set_flow: replaces the flow's schedule/poll triggers; see describe_trigger_kinds."}
						}
					}
				}
			}
		}`,
		[]string{"id"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			body := map[string]any{"ops": args["ops"]}
			if v := stringField(args, "note", ""); v != "" {
				body["note"] = v
			}
			if v := stringField(args, "base", ""); v != "" {
				body["base"] = v
			}
			var out map[string]any
			path := "/me/flows/" + composeFlowID(tenant, workspace, stringField(args, "id", "")) + "/edit"
			if err := c.Post(ctx, path, body, &out); err != nil {
				return errorResultOrErr(err)
			}
			delete(out, "endpoints")
			delete(out, "public_base_configured")
			return TextResult(out), nil
		})
}

func undoFlowEdit(c *DazydClient, d Defaults) Tool {
	return scopedTool(c, d, "undo_flow_edit",
		"Undo an edit_flow: restores the flow to the undo_ref that edit returned. Undoing twice in a row walks back one edit at a time if you pass each edit's undo_ref, newest first.",
		`{"type":"object","required":["id","undo_ref"],"properties":{
			"id":        {"type":"string","description":"Flow ID."},
			"undo_ref":  {"type":"string","description":"The undo_ref from the edit_flow reply to undo."},
			"tenant":    {"type":"string"},
			"workspace": {"type":"string"}
		}}`,
		[]string{"id", "undo_ref"}, true,
		func(ctx context.Context, c *DazydClient, args map[string]any, tenant, workspace string) (ToolCallResult, error) {
			var out map[string]any
			path := "/me/flows/" + composeFlowID(tenant, workspace, stringField(args, "id", "")) + "/restore"
			if err := c.Post(ctx, path, map[string]string{"ref": stringField(args, "undo_ref", "")}, &out); err != nil {
				return errorResultOrErr(err)
			}
			return TextResult(map[string]any{"undone": true, "commit": out["commit"], "canvas_url": out["canvas_url"]}), nil
		})
}
