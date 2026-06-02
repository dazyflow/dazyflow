// Package containerdrop is the substrate-agnostic core of the containerized
// drop runtime (see engine/containerdrop/DESIGN.md). A drop runs as an isolated
// process (the Node drop host) whose ONLY path to the outside world is a
// unix-socket capability broker — the ctx.* surface (fetch / secrets / auth /
// files / log) mediated over a socket instead of in-process function calls.
// Identity is possession of the socket: each execution gets its own broker on
// its own socket.
//
// This package is deliberately runtime-agnostic. How the drop process is
// launched (gVisor, Docker, a plain subprocess) is a pluggable Runner; how the
// drop is packaged (OCI image by digest) is the Runner's concern. The broker
// and the Transport seam are identical across all of them, which is why they
// can be built and tested here without any container runtime.
package containerdrop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/engine/jsdrop"
)

// BrokerDeps are the host-side capability implementations the broker mediates —
// the daemon wires one set, reused across every isolation tier (process/gVisor).
type BrokerDeps struct {
	Secrets map[string]string    // granted secrets (name → value)
	HTTP    jsdrop.HTTPDoer      // guarded HTTP client; nil → http.DefaultClient
	Token   jsdrop.TokenResolver // OAuth token resolver; nil → /token is unavailable
	Files   jsdrop.FileStore     // sandboxed filesystem; nil → /files is unavailable
	OnLog   func(level, msg string)

	// RestrictEgress enforces Egress as the drop's allowed outbound hosts at
	// /fetch (on top of the host HTTP client's own SSRF guard). Empty Egress
	// under RestrictEgress denies all fetch. When false, no per-drop egress
	// check is applied.
	RestrictEgress bool
	Egress         []string
}

// JobContext is what a drop fetches from GET /job at startup.
type JobContext struct {
	Params map[string]any          `json:"params"`
	Inputs map[string]InputRefJSON `json:"inputs"`
	Env    map[string]string       `json:"env"`
	// Secrets are the granted secrets (name→value). Included here so a runtime
	// whose broker calls are async (e.g. the Node drop host) can still expose a
	// SYNCHRONOUS ctx.secrets.get — it reads this prefetched map rather than
	// round-tripping per access. The granted set is already scoped to this job,
	// so eager delivery is no broader than the per-name /secret endpoint.
	Secrets map[string]string `json:"secrets,omitempty"`
}

// InputRefJSON is the wire shape of one input ref handed to the drop.
type InputRefJSON struct {
	MIME  string `json:"mime"`
	Value any    `json:"value,omitempty"`
	Path  string `json:"path,omitempty"`
}

// resultError is the drop-reported typed failure (mirrors jsdrop.DropError).
type resultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Broker serves the capability surface for one drop execution.
type Broker struct {
	ctx  context.Context
	deps BrokerDeps
	job  JobContext

	mu     sync.Mutex
	out    map[string]any
	derr   *resultError
	doneCh chan struct{}
	doneOK bool
}

func newBroker(ctx context.Context, deps BrokerDeps, job JobContext) *Broker {
	return &Broker{ctx: ctx, deps: deps, job: job, doneCh: make(chan struct{})}
}

// handler builds the broker's HTTP routes. JSON in, JSON out.
func (b *Broker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /job", b.handleJob)
	mux.HandleFunc("POST /fetch", b.handleFetch)
	mux.HandleFunc("POST /secret", b.handleSecret)
	mux.HandleFunc("POST /token", b.handleToken)
	mux.HandleFunc("POST /files/read", b.handleFilesRead)
	mux.HandleFunc("POST /files/write", b.handleFilesWrite)
	mux.HandleFunc("POST /files/exists", b.handleFilesExists)
	mux.HandleFunc("POST /log", b.handleLog)
	mux.HandleFunc("POST /result", b.handleResult)
	return mux
}

func (b *Broker) handleJob(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, b.job)
}

type FetchRequest struct {
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	Query        map[string]string `json:"query"`
	Body         string            `json:"body"`
	TimeoutMs    int               `json:"timeoutMs"`
	ExpectStatus []int             `json:"expectStatus"`
}

type FetchResponse struct {
	Status  int               `json:"status"`
	OK      bool              `json:"ok"`
	Headers map[string]string `json:"headers"`
	BodyB64 string            `json:"body_b64"`
}

func (b *Broker) handleFetch(w http.ResponseWriter, r *http.Request) {
	var req FetchRequest
	if !readJSON(w, r, &req) {
		return
	}
	doer := b.deps.HTTP
	if doer == nil {
		doer = http.DefaultClient
	}
	u, err := neturlParse(req.URL, req.Query)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Per-drop egress allowlist: a sandboxed drop may reach only the hosts its
	// manifest declared (on top of the host HTTP client's SSRF guard). This is
	// what bounds an untrusted drop's exfiltration to its stated destinations.
	if b.deps.RestrictEgress {
		host, perr := hostOf(u)
		if perr != nil {
			writeErr(w, http.StatusBadRequest, perr.Error())
			return
		}
		if !egressAllowed(host, b.deps.Egress) {
			writeErr(w, http.StatusForbidden, fmt.Sprintf("egress_denied: %q is not in this drop's declared egress allowlist", host))
			return
		}
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var bodyR io.Reader
	if req.Body != "" {
		bodyR = strings.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(b.ctx, method, u, bodyR)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	if req.TimeoutMs > 0 {
		to := time.Duration(req.TimeoutMs) * time.Millisecond
		if to > maxFetchTimeout {
			to = maxFetchTimeout
		}
		ctx, cancel := context.WithTimeout(b.ctx, to)
		defer cancel()
		hreq = hreq.WithContext(ctx)
	}
	resp, err := doer.Do(hreq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if len(req.ExpectStatus) > 0 && !containsInt(req.ExpectStatus, resp.StatusCode) {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("http_status: %d not in %v", resp.StatusCode, req.ExpectStatus))
		return
	}
	headers := map[string]string{}
	for k := range resp.Header {
		headers[k] = resp.Header.Get(k)
	}
	writeJSON(w, http.StatusOK, FetchResponse{
		Status:  resp.StatusCode,
		OK:      resp.StatusCode >= 200 && resp.StatusCode < 300,
		Headers: headers,
		BodyB64: base64.StdEncoding.EncodeToString(body),
	})
}

func (b *Broker) handleSecret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, ok := b.deps.Secrets[req.Name]
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("secret_denied: %q not granted", req.Name))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

func (b *Broker) handleToken(w http.ResponseWriter, r *http.Request) {
	if b.deps.Token == nil {
		writeErr(w, http.StatusNotImplemented, "auth is not configured")
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Account  string `json:"account"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Mirror native connectors (and the former in-process executor): a drop that
	// calls ctx.auth.token(provider) with no account defaults to the node's
	// params.account, then "default". The tenant rides in b.ctx.
	account := req.Account
	if account == "" {
		if a, ok := b.job.Params["account"].(string); ok && a != "" {
			account = a
		} else {
			account = "default"
		}
	}
	tok, err := b.deps.Token(b.ctx, req.Provider, account)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (b *Broker) handleFilesRead(w http.ResponseWriter, r *http.Request) {
	if b.deps.Files == nil {
		writeErr(w, http.StatusNotImplemented, "files is not configured")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	data, err := b.deps.Files.Read(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"data_b64": base64.StdEncoding.EncodeToString(data)})
}

func (b *Broker) handleFilesWrite(w http.ResponseWriter, r *http.Request) {
	if b.deps.Files == nil {
		writeErr(w, http.StatusNotImplemented, "files is not configured")
		return
	}
	var req struct {
		Path    string `json:"path"`
		DataB64 string `json:"data_b64"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.DataB64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "data_b64: "+err.Error())
		return
	}
	if err := b.deps.Files.Write(req.Path, data); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (b *Broker) handleFilesExists(w http.ResponseWriter, r *http.Request) {
	if b.deps.Files == nil {
		writeErr(w, http.StatusNotImplemented, "files is not configured")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ok, err := b.deps.Files.Exists(req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"exists": ok})
}

func (b *Broker) handleLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if b.deps.OnLog != nil && req.Message != "" {
		b.deps.OnLog(req.Level, req.Message)
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

type resultReq struct {
	Output map[string]any `json:"output"`
	Error  *resultError   `json:"error"`
}

func (b *Broker) handleResult(w http.ResponseWriter, r *http.Request) {
	var req resultReq
	if !readJSON(w, r, &req) {
		return
	}
	b.mu.Lock()
	if !b.doneOK {
		b.out = req.Output
		b.derr = req.Error
		b.doneOK = true
		close(b.doneCh)
	}
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{})
}

// result returns the drop's reported output/error and whether one was received.
func (b *Broker) result() (map[string]any, *resultError, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.out, b.derr, b.doneOK
}

// serveOn runs the broker on ln until the returned stop() is called.
func (b *Broker) serveOn(ln net.Listener) (stop func()) {
	srv := &http.Server{Handler: b.handler()}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}
}

// ── small helpers ─────────────────────────────────────────────────────────

const (
	maxFetchBytes   = 10 << 20
	maxFetchTimeout = 120 * time.Second
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxFetchBytes)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "decode body: "+err.Error())
		return false
	}
	return true
}

// neturlParse parses rawURL and appends the query params (URL-encoded).
func neturlParse(rawURL string, query map[string]string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("bad url: %w", err)
	}
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func containsInt(s []int, n int) bool {
	for _, v := range s {
		if v == n {
			return true
		}
	}
	return false
}

// hostOf extracts the lowercase hostname (no port) from an already-parsed URL
// string for egress matching.
func hostOf(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("bad url: %w", err)
	}
	h := u.Hostname()
	if h == "" {
		return "", fmt.Errorf("url has no host")
	}
	return strings.ToLower(h), nil
}

// egressAllowed reports whether host matches any allowlist entry. An entry is
// an exact hostname or a "*.example.com" wildcard matching any subdomain (but
// not the bare apex). An empty allowlist denies everything — the least-privilege
// default for a drop that declared no egress.
func egressAllowed(host string, allow []string) bool {
	for _, entry := range allow {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if suffix, ok := strings.CutPrefix(entry, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}
