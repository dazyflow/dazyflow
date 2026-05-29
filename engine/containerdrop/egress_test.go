package containerdrop

import (
	"context"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// fetchProbe is a runner that, in-process, dials the broker and attempts one
// fetch — reporting the broker's verdict back as the drop result. It lets the
// egress test drive the broker's /fetch path without a subprocess.
func fetchProbe(url string) RunnerFunc {
	return func(ctx context.Context, socketPath string, _ DropRef) error {
		c := NewClient(socketPath)
		status, ok, _, _, err := c.Fetch(ctx, FetchRequest{URL: url})
		if err != nil {
			return c.Fail(ctx, "fetch", err.Error())
		}
		return c.Result(ctx, map[string]any{"ok": ok, "status": status})
	}
}

func TestEgressAllowlist(t *testing.T) {
	host := testHost(&stubDoer{body: "OK"}, &memFS{m: map[string][]byte{}})
	run := func(t *testing.T, egress []string, url string) core.Result {
		tr := NewTransport(
			core.Manifest{ID: "d"},
			DropRef{ID: "d", RestrictEgress: true, Egress: egress},
			fetchProbe(url),
			host,
		)
		res, err := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
		if err != nil {
			t.Fatalf("execute: %v", err)
		}
		return res
	}

	// Exact-host match → allowed.
	if res := run(t, []string{"api.example.com"}, "https://api.example.com/v1/x"); res.Status != core.StatusOK {
		t.Errorf("declared host should be allowed; got %v %+v", res.Status, res.Error)
	}
	// Wildcard subdomain match → allowed.
	if res := run(t, []string{"*.example.com"}, "https://eu.api.example.com/x"); res.Status != core.StatusOK {
		t.Errorf("subdomain of declared wildcard should be allowed; got %v %+v", res.Status, res.Error)
	}
	// Undeclared host → denied with egress_denied.
	res := run(t, []string{"api.example.com"}, "https://evil.test/exfil")
	if res.Status != core.StatusError {
		t.Fatalf("undeclared host should be denied; got %v", res.Status)
	}
	if res.Error == nil || !strings.Contains(res.Error.Message, "egress_denied") {
		t.Errorf("expected egress_denied, got %+v", res.Error)
	}
	// Empty allowlist under RestrictEgress → deny everything (least privilege).
	res = run(t, nil, "https://api.example.com/x")
	if res.Status != core.StatusError || res.Error == nil || !strings.Contains(res.Error.Message, "egress_denied") {
		t.Errorf("empty allowlist must deny all; got %v %+v", res.Status, res.Error)
	}
}

// Without RestrictEgress (the in-process-equivalent / trusted path), no per-drop
// egress check applies — the host HTTP client's own SSRF guard is the boundary.
func TestEgress_NotRestricted_AllowsAll(t *testing.T) {
	host := testHost(&stubDoer{body: "OK"}, &memFS{m: map[string][]byte{}})
	tr := NewTransport(
		core.Manifest{ID: "d"},
		DropRef{ID: "d"}, // RestrictEgress false
		fetchProbe("https://anything.test/x"),
		host,
	)
	res, err := tr.Execute(context.Background(), core.Job{ID: "j"}, make(chan core.Progress, 1))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Errorf("unrestricted fetch should pass; got %v %+v", res.Status, res.Error)
	}
}
