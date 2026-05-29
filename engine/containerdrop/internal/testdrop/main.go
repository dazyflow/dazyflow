// Command testdrop is a minimal drop process used by the ProcessRunner test: it
// connects to the broker over the unix socket named in HZ_BROKER_SOCKET, reads
// the job, and echoes a param back as its result. It stands in for a real
// containerized drop — proving the cross-process broker boundary works.
package main

import (
	"context"
	"fmt"
	"os"

	"git.sr.ht/~klahr/hazy-flow/engine/containerdrop"
)

func main() {
	ctx := context.Background()
	c := containerdrop.NewClient(os.Getenv(containerdrop.EnvBrokerSocket))
	job, err := c.Job(ctx)
	if err != nil {
		_ = c.Fail(ctx, "job", err.Error())
		os.Exit(1)
	}
	if err := c.Result(ctx, map[string]any{"out": "echo:" + fmt.Sprint(job.Params["p"])}); err != nil {
		os.Exit(1)
	}
}
