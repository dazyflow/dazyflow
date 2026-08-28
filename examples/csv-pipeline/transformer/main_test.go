// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"testing"

	nodepb "git.sr.ht/~klahr/dazyflow/api/gen/node"
	"google.golang.org/grpc"
)

// fakeExecStream captures events the server sends. It satisfies
// nodepb.NodeService_ExecuteServer (grpc.ServerStreamingServer[Event]) by
// embedding grpc.ServerStream for the unused methods.
type fakeExecStream struct {
	grpc.ServerStream
	events []*nodepb.Event
}

func (f *fakeExecStream) Send(e *nodepb.Event) error {
	f.events = append(f.events, e)
	return nil
}

func (f *fakeExecStream) result() *nodepb.Result {
	for _, e := range f.events {
		if r := e.GetResult(); r != nil {
			return r
		}
	}
	return nil
}

func TestListManifests(t *testing.T) {
	s := &server{}
	res, err := s.ListManifests(context.Background(), &nodepb.ListManifestsRequest{})
	if err != nil {
		t.Fatalf("ListManifests: %v", err)
	}
	if len(res.Manifests) != 1 {
		t.Fatalf("manifests=%d, want 1", len(res.Manifests))
	}
	m := res.Manifests[0]
	if m.Id != "csv_uppercase" {
		t.Fatalf("id=%q", m.Id)
	}
	if len(m.Inputs) != 1 || !m.Inputs[0].Required {
		t.Fatalf("inputs=%+v", m.Inputs)
	}
	if len(m.Outputs) != 1 {
		t.Fatalf("outputs=%+v", m.Outputs)
	}
}

func TestExecuteUppercasesJSONString(t *testing.T) {
	s := &server{}
	inline, _ := json.Marshal("hello world")
	stream := &fakeExecStream{}
	job := &nodepb.Job{
		JobId:  "j1",
		NodeId: "n1",
		Input:  map[string]*nodepb.Ref{"in": {Inline: inline}},
	}
	if err := s.Execute(job, stream); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r := stream.result()
	if r == nil || r.Status != "ok" {
		t.Fatalf("result=%+v", r)
	}
	out := r.Output["out"]
	if out == nil {
		t.Fatalf("no out ref")
	}
	var got string
	if err := json.Unmarshal(out.Inline, &got); err != nil {
		t.Fatalf("decode out: %v", err)
	}
	if got != "HELLO WORLD" {
		t.Fatalf("got=%q", got)
	}
	if out.Mime != "text/csv" {
		t.Fatalf("mime=%q", out.Mime)
	}
	// progress event should have been sent before result
	var sawProgress bool
	for _, e := range stream.events {
		if e.GetProgress() != nil {
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Fatalf("no progress event")
	}
}

func TestExecuteRawBytes(t *testing.T) {
	s := &server{}
	stream := &fakeExecStream{}
	job := &nodepb.Job{
		JobId: "j2",
		Input: map[string]*nodepb.Ref{"in": {Inline: []byte("abc")}},
	}
	if err := s.Execute(job, stream); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r := stream.result()
	var got string
	_ = json.Unmarshal(r.Output["out"].Inline, &got)
	if got != "ABC" {
		t.Fatalf("got=%q", got)
	}
}

func TestExecuteMissingInput(t *testing.T) {
	s := &server{}
	stream := &fakeExecStream{}
	job := &nodepb.Job{JobId: "j3", Input: map[string]*nodepb.Ref{}}
	if err := s.Execute(job, stream); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	r := stream.result()
	if r == nil || r.Status != "error" || r.Error.Code != "missing_input" {
		t.Fatalf("result=%+v", r)
	}
}

func TestDecodeInlineText(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"empty", nil, ""},
		{"json-quoted", []byte(`"hi"`), "hi"},
		{"raw", []byte("plain"), "plain"},
		{"bad-json-quote-falls-back", []byte(`"unterminated`), `"unterminated`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := decodeInlineText(c.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Fatalf("got=%q want=%q", got, c.want)
			}
		})
	}
}
