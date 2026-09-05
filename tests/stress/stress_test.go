// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// A load rig for the execution path: the queue, the dispatcher, the workers and
// the event bus, against a real Postgres, with several simulated replicas
// contending for the same work.
//
// Contention is the whole point. A single-process benchmark says how fast one
// worker pool drains a queue; it cannot say what happens when four pools claim
// from the same table at once, which is the shape of every deployment past one
// pod and the thing no test here covered.
//
// See README.md for what to turn and how to read the result.

package stress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/daemon"
	"github.com/dazyflow/dazyflow/engine"
	"github.com/dazyflow/dazyflow/engine/jobstore"
)

type params struct {
	replicas, workers, conns int
	rate, seconds            int
	nodes, stepMS, tenants   int
	// hogRuns is a burst one extra org dumps on the queue at the start — the
	// fairness question: how long does everyone else wait behind it? Each of
	// its runs is hogWidth INDEPENDENT steps, all queued at once; a chain
	// meters itself and hogs nothing.
	hogRuns, hogWidth int
}

func load() params {
	p := params{replicas: 3, workers: 8, conns: 20, rate: 20, seconds: 60, nodes: 8, stepMS: 50, tenants: 50, hogWidth: 64}
	for _, f := range []struct {
		env string
		dst *int
	}{
		{"STRESS_REPLICAS", &p.replicas}, {"STRESS_WORKERS", &p.workers},
		{"STRESS_CONNS", &p.conns}, {"STRESS_RATE", &p.rate},
		{"STRESS_SECONDS", &p.seconds}, {"STRESS_NODES", &p.nodes},
		{"STRESS_STEP_MS", &p.stepMS}, {"STRESS_TENANTS", &p.tenants},
		{"STRESS_HOG_RUNS", &p.hogRuns}, {"STRESS_HOG_WIDTH", &p.hogWidth},
	} {
		if v := os.Getenv(f.env); v != "" {
			fmt.Sscan(v, f.dst)
		}
	}
	return p
}

// stressStep occupies its worker for the configured time and touches nothing
// external — so what the rig measures is dazyflow, not somebody's API.
//
// It cannot be the shipped `delay` drop: that one deliberately hands its slot
// back and asks to be re-claimed at the deadline, so a queue full of them
// measures queue overhead and reports throughput a real connector could never
// reach.
const stressStepModule = "zz_stress_step"

var registerStep sync.Once

func registerStressStep() {
	registerStep.Do(func() {
		engine.Register(engine.NativeDrop{
			Manifest: core.Manifest{
				ID: stressStepModule, Version: "1", Label: "Stress step",
				Summary:  "Occupies a worker for ms milliseconds. Load rig only.",
				Examples: []core.ParamsExample{{Title: "50ms", Params: json.RawMessage(`{"ms":50}`)}},
				Inputs:   []core.Port{{Port: core.PassPort}},
				Outputs:  []core.Port{{Port: core.PassPort}},
			},
			Execute: func(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
				ms := 0
				switch v := job.Params["ms"].(type) {
				case float64:
					ms = int(v)
				case int:
					ms = v
				}
				select {
				case <-time.After(time.Duration(ms) * time.Millisecond):
				case <-ctx.Done():
					return core.Result{Status: core.StatusError}, ctx.Err()
				}
				return core.Result{
					JobID:  job.ID,
					Status: core.StatusOK,
					Output: map[string]core.Ref{core.PassPort: {MIME: "application/json", Inline: "ok"}},
				}, nil
			},
		})
	})
}

// replica is one simulated dzd: its own pool, stores and worker pool, so the
// contention and the pool statistics are per-process exactly as in a fleet.
type replica struct {
	id   int
	pool *pgxpool.Pool
	jobs *jobstore.Postgres
	svc  *daemon.Service
	bus  *daemon.PgBus // nil under STRESS_BUS=memory
}

func newReplica(t *testing.T, ctx context.Context, dsn string, id int, p params, trace *stmtCounter) *replica {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = int32(p.conns)
	cfg.MinConns = 2
	if trace != nil {
		cfg.ConnConfig.Tracer = trace
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := jobstore.NewPostgresFromPool(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	// STRESS_BURST_SPACING overrides the queue's burst spacing ("0" for plain
	// FIFO), to see what fairness costs and what it buys.
	if v := os.Getenv("STRESS_BURST_SPACING"); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			t.Fatalf("STRESS_BURST_SPACING: %v", perr)
		}
		jobs.SetBurstSpacing(d)
	}
	// STRESS_BUS=memory takes the event bus off the database entirely, to see
	// what it is costing. Not a supported deployment — a memory bus reaches no
	// other replica — purely an attribution knob.
	var bus daemon.Bus
	if os.Getenv("STRESS_BUS") == "memory" {
		bus = daemon.NewMemoryBus()
	} else {
		pgb, berr := daemon.NewPgBus(ctx, pool)
		if berr != nil {
			t.Fatal(berr)
		}
		bus = pgb
	}
	eng := &engine.Engine{Resolver: &engine.NodeResolver{Native: engine.Default}}
	svc := &daemon.Service{
		Jobs: jobs, Bus: bus, Engine: eng,
		WorkerID: fmt.Sprintf("replica-%d", id),
	}
	runs := daemon.NewRunCache(0) // one per process, as dzd wires it
	for w := range p.workers {
		worker := daemon.NewWorker(daemon.WorkerConfig{
			ID:           fmt.Sprintf("r%d-w%d", id, w),
			PollInterval: 25 * time.Millisecond,
			MaxRetries:   1,
			Logger:       quiet(),
			Runs:         runs,
		}, jobs, eng, bus)
		worker.SubGraphRunner = svc
		go func() { _ = worker.Run(ctx) }()
	}
	r := &replica{id: id, pool: pool, jobs: jobs, svc: svc}
	if pgb, ok := bus.(*daemon.PgBus); ok {
		r.bus = pgb
	}
	return r
}

// hogGraph is one org fanning out: hogWidth steps with no wires between them,
// so a single submit puts all of them on the queue at once.
func hogGraph(p params, tenant string) core.Graph {
	g := core.Graph{ID: "hog", Tenant: tenant, Workspace: "main"}
	for i := range p.hogWidth {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("h%d", i), Module: stressStepModule,
			Params: map[string]any{"ms": p.stepMS},
		})
	}
	return g
}

func stressGraph(p params, tenant string) core.Graph {
	g := core.Graph{ID: "stress", Tenant: tenant, Workspace: "main"}
	for i := range p.nodes {
		g.Nodes = append(g.Nodes, core.Node{
			ID: fmt.Sprintf("n%d", i), Module: stressStepModule,
			Params: map[string]any{"ms": p.stepMS},
		})
		if i > 0 {
			g.Edges = append(g.Edges, core.Edge{
				From: fmt.Sprintf("n%d", i-1), FromPort: core.PassPort,
				To: fmt.Sprintf("n%d", i), ToPort: core.PassPort,
			})
		}
	}
	return g
}

func TestStressQueue(t *testing.T) {
	dsn := os.Getenv("STRESS_DSN")
	if dsn == "" {
		t.Skip("set STRESS_DSN to a SCRATCH database — the rig truncates jobs and bus_events")
	}
	p := load()
	registerStressStep()
	ctx, cancel := context.WithCancel(context.Background())

	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := jobstore.NewPostgresFromPool(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := daemon.NewPgBus(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "TRUNCATE jobs, bus_events"); err != nil {
		t.Fatal(err)
	}

	// STRESS_TRACE=1 counts the statements the run issues, so the round trips a
	// step costs can be attributed rather than estimated. It adds a little
	// overhead, so it is off unless asked for.
	var trace *stmtCounter
	if os.Getenv("STRESS_TRACE") != "" {
		trace = newStmtCounter()
	}
	replicas := make([]*replica, p.replicas)
	for i := range replicas {
		replicas[i] = newReplica(t, ctx, dsn, i, p, trace)
	}
	// Cancel BEFORE closing: each replica's bus listener holds a pooled
	// connection until its context ends, and Close waits for it. The bus
	// writer flushes once more on cancel; let it finish before the pool goes.
	defer func() {
		cancel()
		for _, r := range replicas {
			if r.bus != nil {
				r.bus.Close()
			}
			r.pool.Close()
		}
	}()
	time.Sleep(500 * time.Millisecond) // let the workers settle onto the queue
	if trace != nil {
		trace.reset() // discard schema bootstrap and pool warm-up
	}

	sizeOfJobs := func() int64 {
		var n int64
		_ = admin.QueryRow(ctx, `SELECT pg_total_relation_size('jobs')`).Scan(&n)
		return n
	}
	// Commits per second is what says whether the fleet or the DATABASE is the
	// ceiling: when adding workers stops adding throughput, this is the number
	// that has stopped moving.
	commits := func() int64 {
		var n int64
		_ = admin.QueryRow(ctx,
			`SELECT xact_commit FROM pg_stat_database WHERE datname = current_database()`).Scan(&n)
		return n
	}
	startBytes, startCommits := sizeOfJobs(), commits()

	// Offer runs at a fixed rate, spread across orgs and round-robined across
	// replicas — a load balancer's view.
	//
	// Several submitters share the ticker. SubmitGraph is synchronous and costs
	// a transaction, so one goroutine tops out well below a high target rate
	// and the rig then reports "achieved X% of offered" against a rate it never
	// actually offered. The report below uses the SUBMITTED rate, not the
	// target, for the same reason.
	var submitted, submitFailed atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	ticks := make(chan int, p.rate)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ticks)
		tick := time.NewTicker(time.Second / time.Duration(max(p.rate, 1)))
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
				select {
				case ticks <- i:
				default: // submitters are all busy: the offered rate is what it is
				}
			}
		}
	}()
	const submitters = 8
	for range submitters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ticks {
				r := replicas[i%len(replicas)]
				tenant := fmt.Sprintf("t%03d", i%max(p.tenants, 1))
				princ := daemon.SystemPrincipal("dazyflow-stress", tenant, "main")
				if _, err := r.svc.SubmitGraph(ctx, princ, stressGraph(p, tenant)); err != nil {
					submitFailed.Add(1)
				} else {
					submitted.Add(1)
				}
			}
		}()
	}

	// The hog: one org submits its whole burst at once, alongside everyone
	// else's steady trickle. Its steps are older than theirs from then on, so a
	// FIFO queue serves the burst first and the trickle waits behind it.
	const hogTenant = "hog"
	if p.hogRuns > 0 {
		hogRuns := make(chan int, p.hogRuns)
		for i := range p.hogRuns {
			hogRuns <- i
		}
		close(hogRuns)
		for range submitters {
			wg.Add(1)
			go func() {
				defer wg.Done()
				princ := daemon.SystemPrincipal("dazyflow-stress", hogTenant, "main")
				for i := range hogRuns {
					if _, err := replicas[i%len(replicas)].svc.SubmitGraph(ctx, princ, hogGraph(p, hogTenant)); err != nil {
						submitFailed.Add(1)
					}
				}
			}()
		}
	}

	// Sample while the load runs.
	type sample struct {
		at        time.Time
		done      int
		queued    int
		oldestSec float64
		// The oldest queued step per org, split hog / everyone else.
		hogWait, othersWait float64
	}
	var samples []sample
	deadline := time.Now().Add(time.Duration(p.seconds) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		counts, err := replicas[0].jobs.CountsByStatus(ctx)
		if err != nil {
			continue
		}
		var oldest float64
		if at, ok, err := replicas[0].jobs.OldestQueuedEnqueuedAt(ctx); err == nil && ok {
			oldest = time.Since(at).Seconds()
		}
		smp := sample{
			at:        time.Now(),
			done:      counts[core.JobStatusSucceeded] + counts[core.JobStatusFailed] + counts[core.JobStatusSkipped],
			queued:    counts[core.JobStatusQueued],
			oldestSec: oldest,
		}
		if p.hogRuns > 0 {
			rows, err := admin.Query(ctx, `SELECT tenant, extract(epoch FROM now() - min(enqueued_at))
			                                 FROM jobs WHERE kind = 'node' AND status = 'queued' GROUP BY tenant`)
			if err == nil {
				for rows.Next() {
					var tenant string
					var wait float64
					if rows.Scan(&tenant, &wait) != nil {
						continue
					}
					if tenant == hogTenant {
						smp.hogWait = wait
					} else {
						smp.othersWait = max(smp.othersWait, wait)
					}
				}
				rows.Close()
			}
		}
		samples = append(samples, smp)
	}
	close(stop)
	wg.Wait()

	// Report.
	if len(samples) < 2 {
		t.Fatal("not enough samples; raise STRESS_SECONDS")
	}
	first, last := samples[0], samples[len(samples)-1]
	span := last.at.Sub(first.at).Seconds()
	stepsPerSec := float64(last.done-first.done) / span
	targetSteps := float64(p.rate * p.nodes)
	// What was really put on the queue, as opposed to what was asked for.
	offeredSteps := float64(submitted.Load()*int64(p.nodes)) / float64(p.seconds)

	var maxOldest, sumOldest float64
	maxQueued := 0
	for _, s := range samples {
		if s.oldestSec > maxOldest {
			maxOldest = s.oldestSec
		}
		sumOldest += s.oldestSec
		if s.queued > maxQueued {
			maxQueued = s.queued
		}
	}

	var emptyAcquires int64
	var acquireWait time.Duration
	for _, r := range replicas {
		st := r.pool.Stat()
		emptyAcquires += st.EmptyAcquireCount()
		acquireWait += st.AcquireDuration()
	}
	grew := sizeOfJobs() - startBytes
	commitsPerSec := float64(commits()-startCommits) / span
	runs := submitted.Load()

	t.Logf("")
	t.Logf("  fleet         %d replicas x %d workers = %d concurrent steps, %d conns each",
		p.replicas, p.workers, p.replicas*p.workers, p.conns)
	t.Logf("  load          target %d runs/s x %d steps = %.0f steps/s, %dms each, %d orgs",
		p.rate, p.nodes, targetSteps, p.stepMS, p.tenants)
	t.Logf("  submitted     %.0f steps/s actually offered   (%.0f%% of target — below 100%% the SUBMITTER is the limit)",
		offeredSteps, 100*offeredSteps/max(targetSteps, 1))
	t.Logf("  ---")
	t.Logf("  achieved      %.0f steps/s   (%.0f%% of what was offered)", stepsPerSec, 100*stepsPerSec/max(offeredSteps, 1))
	t.Logf("  theoretical   %.0f steps/s   (%d workers / %dms)",
		float64(p.replicas*p.workers)*1000/float64(max(p.stepMS, 1)), p.replicas*p.workers, p.stepMS)
	t.Logf("  queue latency %.2fs mean, %.2fs worst   (oldest step still waiting)", sumOldest/float64(len(samples)), maxOldest)
	t.Logf("  backlog       %d steps queued at worst", maxQueued)
	t.Logf("  pool          %d empty acquires, %s cumulative wait, across the fleet", emptyAcquires, acquireWait.Round(time.Millisecond))
	t.Logf("  database      %.0f commits/s, %.1f per step   (flat while achieved is flat = the DB is the ceiling)",
		commitsPerSec, commitsPerSec/max(stepsPerSec, 1))
	t.Logf("  storage       %.1f MB for %d runs = %.1f KB/run", float64(grew)/1e6, runs, float64(grew)/float64(max(runs, 1))/1e3)
	if p.hogRuns > 0 {
		var hogWorst, othersWorst, othersSum float64
		for _, s := range samples {
			hogWorst = max(hogWorst, s.hogWait)
			othersWorst = max(othersWorst, s.othersWait)
			othersSum += s.othersWait
		}
		t.Logf("  hog           one org burst %d runs x %d independent steps = %d steps at the start", p.hogRuns, p.hogWidth, p.hogRuns*p.hogWidth)
		t.Logf("  everyone else waited %.2fs at worst, %.2fs mean   (the hog itself: %.2fs at worst)",
			othersWorst, othersSum/float64(len(samples)), hogWorst)
	}
	if f := submitFailed.Load(); f > 0 {
		t.Logf("  REFUSED       %d submissions failed", f)
	}
	if trace != nil {
		steps := float64(last.done - first.done)
		t.Logf("  ---")
		t.Logf("  statements    %.1f per step, by shape:", float64(trace.total())/max(steps, 1))
		for _, line := range trace.top() {
			per := float64(line.n) / max(steps, 1)
			if per < 0.02 {
				continue
			}
			t.Logf("      %5.2f/step  %-28s (%d)", per, line.shape, line.n)
		}
	}
	t.Logf("")
}

func quiet() *log.Logger { return log.New(io.Discard, "", 0) }
