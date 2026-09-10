// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Command transformer is a standalone gRPC NodeService that uppercases
// the text content of its input ref and emits the result on its "out"
// port. It exists to demonstrate the end-to-end flow of building a
// remote module — what code a third party would write to plug into
// Dazyflow.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"strings"

	"google.golang.org/grpc"

	nodepb "github.com/dazyflow/dazyflow/api/gen/node"
)

type server struct {
	nodepb.UnimplementedNodeServiceServer
}

func (s *server) ListManifests(_ context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
	return &nodepb.ListManifestsResponse{Manifests: []*nodepb.Manifest{{
		Id:             "csv_uppercase",
		Version:        "1.0",
		Label:          "CSV Uppercase",
		Color:          "#a050a0",
		ExecutionModel: "batch",
		ProcessModel:   "long_lived",
		Inputs:         []*nodepb.Port{{Id: "in", Required: true}},
		Outputs:        []*nodepb.Port{{Id: "out"}},
		Idempotent:     true,
		// Presentation. Without these the step shows up in the palette as a
		// generic box that search cannot find by concept.
		Icon:        "type",
		Category:    "transformation",
		Subtitle:    "Upper-case every cell",
		Summary:     "Upper-cases every cell of a CSV.",
		Description: "Reads a CSV value, upper-cases every cell, and returns the result. A worked example of a runner: the transformation itself is trivial so the wiring is what you read.",
		Tags:        []string{"csv", "uppercase", "example"},
	}}}, nil
}

func (s *server) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
	in, ok := job.Input["in"]
	if !ok {
		return sendResult(stream, job.JobId, &nodepb.Result{
			JobId:  job.JobId,
			Status: "error",
			Error:  &nodepb.JobError{Code: "missing_input", Message: "input port 'in' missing"},
		})
	}

	text, err := decodeInlineText(in.Inline)
	if err != nil {
		return sendResult(stream, job.JobId, &nodepb.Result{
			JobId:  job.JobId,
			Status: "error",
			Error:  &nodepb.JobError{Code: "decode_input", Message: err.Error()},
		})
	}

	_ = stream.Send(&nodepb.Event{
		Payload: &nodepb.Event_Progress{Progress: &nodepb.Progress{
			JobId:   job.JobId,
			NodeId:  job.NodeId,
			Percent: 0.5,
			Message: fmt.Sprintf("uppercasing %d bytes", len(text)),
		}},
	})

	upper := strings.ToUpper(text)
	encoded, _ := json.Marshal(upper) // re-wrap so engine.refFromPB recovers a string

	return sendResult(stream, job.JobId, &nodepb.Result{
		JobId:  job.JobId,
		Status: "ok",
		Output: map[string]*nodepb.Ref{
			"out": {Mime: "text/csv", Inline: encoded},
		},
	})
}

func decodeInlineText(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s, nil
		}
	}
	return string(raw), nil
}

func sendResult(stream nodepb.NodeService_ExecuteServer, _ string, r *nodepb.Result) error {
	return stream.Send(&nodepb.Event{Payload: &nodepb.Event_Result{Result: r}})
}

func main() {
	addr := flag.String("listen", "127.0.0.1:18001", "gRPC listen address")
	flag.Parse()
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	srv := grpc.NewServer()
	nodepb.RegisterNodeServiceServer(srv, &server{})
	log.Printf("csv_uppercase listening on %s", lis.Addr())
	if err := srv.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}
