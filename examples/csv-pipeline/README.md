# csv-pipeline — end-to-end demo

A 3-node Dazyflow graph that runs across **two processes**:

```
   ┌──────────────────────────────── dzd (native modules) ───────────┐
   │                                                                 │
   │   file_read ──────────────────────────────→ file_write          │
   │   (workspace sandbox)                       (workspace sandbox) │
   │                       ↘                ↗                        │
   └────────────────────────┘────────────────┘──────────────────────┘
                           gRPC│              │gRPC
                               ↓              │
                       ┌────────────────────────┐
                       │  csv_uppercase (remote)│
                       │  port :60001           │
                       └────────────────────────┘
```

Inputs are pulled from a sandboxed file, handed off to an out-of-process
gRPC module, the result streamed back, and written to a different file
within the same sandbox.

## Run it

```
./run.sh
```

That script builds three binaries (`dzd`, `dzctl`, `transformer`), boots
both servers, submits `pipeline.json` over the mTLS-disabled-for-dev
control API, and asserts the output matches.

## What the demo proves

| Capability | Where it's exercised |
|---|---|
| Distributed worker pool | dzd spins 2 workers; node `transform` lands on whichever claims first |
| Native ↔ remote interop | file_read (Go, native) → csv_uppercase (Go, gRPC) → file_write (Go, native) |
| Inline data over gRPC | file_read in `inline:true` mode embeds the file content into the Ref, dzd serializes it through the engine's protobuf adapter |
| Sandbox containment | `file_read` and `file_write` both operate on `$SANDBOX_BASE/dev/main/`; absolute paths or `..` would be rejected |
| Progress streaming | The `[transform] 50% uppercasing 52 bytes` line travels remote → engine → gRPC stream → dzctl |
| Audit | The graph + each node have JobRecords with worker IDs, timestamps, attempts |

## What broke (the friction this demo was built to find)

1. **`file_read` couldn't inline content.** Without this, the remote
   module has no way to read a file it doesn't share a filesystem with.
   Added an `inline: true` param and MIME-aware string/byte handling.

2. **`dzd` had no way to register remote modules.** `engine.RemoteCatalog`
   existed but was never instantiated. Added `DAZYFLOW_REMOTE_MODULES=id=host:port`.

3. **Binary inline data is wonky.** The engine wraps `Ref.Inline` with
   `json.Marshal` for gRPC transport. Text round-trips fine if you put
   a `string` in `Inline`; binary `[]byte` becomes base64-encoded and
   the consumer has to decode by hand. Documented in `file_read.go`;
   needs cleanup before binary pipelines are first-class.

## What the demo does *not* prove

- **External triggering.** Submission happens via `dzctl`; nothing fires this
  graph on its own. The platform does have triggers (`drops/trigger/` — cron,
  poll, webhook); this demo just doesn't use one.
- **Conditional routing.** The graph is a fixed chain. `branch` exists
  (`drops/flow/branch.go`) and `examples/ap-invoice` demonstrates it.
- **HTTP integration.** No `http_request`. The transformer is a stub
  that does string manipulation, not a real integration with anything.
- **mTLS on the remote.** `DAZYFLOW_REMOTE_MODULES` dials insecure (the
  RemoteDescriptor sets `Insecure: true`). Production should pass TLS
  config; the engine refuses unencrypted by default but this entry
  point opts in.

## Anatomy: writing your own remote module

The complete remote module is ~110 lines (`transformer/main.go`). The
shape any third-party Go module needs:

```go
type server struct {
    nodepb.UnimplementedNodeServiceServer
}

func (s *server) ListManifests(ctx context.Context, _ *nodepb.ListManifestsRequest) (*nodepb.ListManifestsResponse, error) {
    // declare every drop this runner serves: id, inputs, outputs, idempotency.
    // Serving one drop means returning a list of one.
}

func (s *server) Execute(job *nodepb.Job, stream nodepb.NodeService_ExecuteServer) error {
    // job.DropId says WHICH drop to run — ignorable if you serve only one
    // read job.Input["<port>"].Inline (or job.Input["<port>"].Ref + side-channel)
    // do work, optionally stream progress events
    // send a final Result event
}

func main() {
    grpc.NewServer().Serve(net.Listen("tcp", addr))
}
```

For non-Go modules use the same gRPC service — proto file is in
`api/proto/node.proto`.
