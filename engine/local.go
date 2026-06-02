package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
)

// LocalDescriptor is the on-disk JSON file under hzd.toml's
// modules.descriptor_dir telling the engine how to launch an external
// module as a subprocess.
type LocalDescriptor struct {
	ID      string            `json:"id"`
	Runtime string            `json:"runtime"` // "executable" — others reserved for future
	Path    string            `json:"path"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// LocalTransport runs a module by spawning the executable named in the
// descriptor and exchanging newline-delimited JSON over stdio. Step 6 uses
// spawn-per-job; ProcessLongLived can be added by keeping cmd alive
// between Execute calls.
type LocalTransport struct {
	Descriptor LocalDescriptor
	manifest   core.Manifest
}

func (t *LocalTransport) Manifest() core.Manifest { return t.manifest }

func (t *LocalTransport) Execute(ctx context.Context, job core.Job, progress chan<- core.Progress) (core.Result, error) {
	cmd := exec.CommandContext(ctx, t.Descriptor.Path, t.Descriptor.Args...)
	cmd.Env = os.Environ()
	for k, v := range t.Descriptor.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return localErr(job, "spawn", err), err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return localErr(job, "spawn", err), err
	}
	if err := cmd.Start(); err != nil {
		return localErr(job, "spawn", err), err
	}

	result, protoErr := runProtocol(ctx, job, stdin, stdout, progress, t.Descriptor.ID)
	_ = stdin.Close()
	waitErr := cmd.Wait()

	if protoErr != nil {
		return result, protoErr
	}
	if waitErr != nil && !errors.Is(ctx.Err(), context.Canceled) {
		return result, fmt.Errorf("subprocess %s: %w", t.Descriptor.ID, waitErr)
	}
	return result, nil
}

// LocalCatalog stores LocalTransports keyed by module ID. On Register it
// handshakes the binary to cache its manifest so the engine can validate
// graphs without spawning per node.
type LocalCatalog struct {
	HandshakeTimeout time.Duration

	mu    sync.RWMutex
	nodes map[string]*LocalTransport
}

func NewLocalCatalog() *LocalCatalog {
	return &LocalCatalog{
		HandshakeTimeout: 5 * time.Second,
		nodes:            make(map[string]*LocalTransport),
	}
}

func (c *LocalCatalog) Register(desc LocalDescriptor) error {
	if desc.Runtime != "" && desc.Runtime != "executable" {
		return fmt.Errorf("descriptor %q: unsupported runtime %q (only 'executable' is implemented)", desc.ID, desc.Runtime)
	}
	timeout := c.HandshakeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	manifest, err := handshake(ctx, desc)
	if err != nil {
		return fmt.Errorf("descriptor %q handshake: %w", desc.ID, err)
	}
	if manifest.ID != desc.ID {
		return fmt.Errorf("descriptor %q reported manifest id %q", desc.ID, manifest.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes[desc.ID] = &LocalTransport{Descriptor: desc, manifest: manifest}
	return nil
}

// LoadDir reads every *.json file in dir, parses it as a LocalDescriptor,
// and registers it. Errors are collected and returned as a joined error so
// one broken descriptor doesn't block the rest.
func (c *LocalCatalog) LoadDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %q: %w", dir, err)
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		var desc LocalDescriptor
		if err := json.Unmarshal(data, &desc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		if err := c.Register(desc); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func (c *LocalCatalog) Get(id string) (core.Transport, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.nodes[id]
	if !ok {
		return nil, false
	}
	return t, true
}

func (c *LocalCatalog) Manifests() map[string]core.Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]core.Manifest, len(c.nodes))
	for id, t := range c.nodes {
		out[id] = t.manifest
	}
	return out
}

func handshake(ctx context.Context, desc LocalDescriptor) (core.Manifest, error) {
	cmd := exec.CommandContext(ctx, desc.Path, desc.Args...)
	cmd.Env = os.Environ()
	for k, v := range desc.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return core.Manifest{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return core.Manifest{}, err
	}
	if err := cmd.Start(); err != nil {
		return core.Manifest{}, err
	}

	enc := json.NewEncoder(stdin)
	if err := enc.Encode(helloMessage()); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return core.Manifest{}, fmt.Errorf("send hello: %w", err)
	}
	dec := json.NewDecoder(bufio.NewReader(stdout))
	manifest, err := readManifest(dec)

	_ = stdin.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return manifest, err
}

func localErr(job core.Job, code string, err error) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusError,
		Error:  &core.JobError{Code: code, Message: err.Error()},
	}
}

// ----------------------------------------------------------------- protocol

func helloMessage() map[string]any {
	return map[string]any{
		"type":           "hello",
		"protocol":       "1.0",
		"engine_version": "1.0",
	}
}

type manifestEnvelope struct {
	Type string `json:"type"`
	core.Manifest
}

func readManifest(dec *json.Decoder) (core.Manifest, error) {
	var env manifestEnvelope
	if err := dec.Decode(&env); err != nil {
		return core.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if env.Type != "manifest" {
		return core.Manifest{}, fmt.Errorf("expected manifest, got %q", env.Type)
	}
	return env.Manifest, nil
}

// runProtocol drives one execution against an already-spawned module. It
// is separated from Execute so unit tests can use io.Pipe instead of a
// real subprocess.
func runProtocol(
	ctx context.Context,
	job core.Job,
	stdin io.Writer,
	stdout io.Reader,
	progress chan<- core.Progress,
	expectedID string,
) (core.Result, error) {
	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(bufio.NewReader(stdout))

	if err := enc.Encode(helloMessage()); err != nil {
		return core.Result{}, fmt.Errorf("send hello: %w", err)
	}
	manifest, err := readManifest(dec)
	if err != nil {
		return core.Result{}, err
	}
	if expectedID != "" && manifest.ID != expectedID {
		return core.Result{}, fmt.Errorf("module id mismatch: got %q, want %q", manifest.ID, expectedID)
	}

	if err := enc.Encode(executeMessage(job)); err != nil {
		return core.Result{}, fmt.Errorf("send execute: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return core.Result{}, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return core.Result{}, fmt.Errorf("read response: %w", err)
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return core.Result{}, fmt.Errorf("parse response type: %w", err)
		}
		switch head.Type {
		case "progress":
			if progress == nil {
				continue
			}
			var p core.Progress
			if err := json.Unmarshal(raw, &p); err != nil {
				continue
			}
			select {
			case progress <- p:
			case <-ctx.Done():
				return core.Result{}, ctx.Err()
			}
		case "result":
			var r core.Result
			if err := json.Unmarshal(raw, &r); err != nil {
				return core.Result{}, fmt.Errorf("parse result: %w", err)
			}
			return r, nil
		default:
			// unknown message; ignore
		}
	}
}

func executeMessage(job core.Job) map[string]any {
	return map[string]any{
		"type":   "execute",
		"job_id": job.ID,
		"input":  job.Input,
		"params": job.Params,
		"output": job.Output,
	}
}
