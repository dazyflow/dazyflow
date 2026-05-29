package containerdrop

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
)

// Client is the drop-side view of the broker: a small HTTP client that dials the
// broker's unix socket. It's the Go reference implementation of the broker
// protocol — used by the tests (as an in-process drop) and reusable by a
// Go-based Runner. The in-container JS client (a thin wrapper around the same
// endpoints) is the runtime-side counterpart, built when the JS-in-container
// runtime lands.
type Client struct {
	http *http.Client
}

// NewClient dials the broker listening on socketPath.
func NewClient(socketPath string) *Client {
	return &Client{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

func (c *Client) do(ctx context.Context, method, path string, reqBody, respOut any) error {
	var r io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://broker"+path, r)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("broker %s %s: %s", method, path, e.Error)
	}
	if respOut != nil {
		return json.Unmarshal(body, respOut)
	}
	return nil
}

// Job fetches the job context (params, inputs, env).
func (c *Client) Job(ctx context.Context) (JobContext, error) {
	var j JobContext
	err := c.do(ctx, http.MethodGet, "/job", nil, &j)
	return j, err
}

// Fetch performs a guarded HTTP request through the broker; the response body is
// decoded from base64 for the caller.
func (c *Client) Fetch(ctx context.Context, req FetchRequest) (status int, ok bool, headers map[string]string, body []byte, err error) {
	var resp FetchResponse
	if err = c.do(ctx, http.MethodPost, "/fetch", req, &resp); err != nil {
		return 0, false, nil, nil, err
	}
	body, err = base64.StdEncoding.DecodeString(resp.BodyB64)
	return resp.Status, resp.OK, resp.Headers, body, err
}

// Secret returns a granted secret's value.
func (c *Client) Secret(ctx context.Context, name string) (string, error) {
	var out struct {
		Value string `json:"value"`
	}
	err := c.do(ctx, http.MethodPost, "/secret", map[string]string{"name": name}, &out)
	return out.Value, err
}

// Token returns an OAuth token for (provider, account).
func (c *Client) Token(ctx context.Context, provider, account string) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	err := c.do(ctx, http.MethodPost, "/token", map[string]string{"provider": provider, "account": account}, &out)
	return out.Token, err
}

// ReadFile reads a sandboxed file's bytes.
func (c *Client) ReadFile(ctx context.Context, path string) ([]byte, error) {
	var out struct {
		DataB64 string `json:"data_b64"`
	}
	if err := c.do(ctx, http.MethodPost, "/files/read", map[string]string{"path": path}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.DataB64)
}

// WriteFile writes bytes to a sandboxed file.
func (c *Client) WriteFile(ctx context.Context, path string, data []byte) error {
	return c.do(ctx, http.MethodPost, "/files/write", map[string]string{
		"path": path, "data_b64": base64.StdEncoding.EncodeToString(data),
	}, nil)
}

// Exists reports whether a sandboxed file exists.
func (c *Client) Exists(ctx context.Context, path string) (bool, error) {
	var out struct {
		Exists bool `json:"exists"`
	}
	err := c.do(ctx, http.MethodPost, "/files/exists", map[string]string{"path": path}, &out)
	return out.Exists, err
}

// Log forwards a log line to the host (surfaces on the run's progress stream).
func (c *Client) Log(ctx context.Context, level, message string) error {
	return c.do(ctx, http.MethodPost, "/log", map[string]string{"level": level, "message": message}, nil)
}

// Result reports the drop's successful output ports.
func (c *Client) Result(ctx context.Context, output map[string]any) error {
	return c.do(ctx, http.MethodPost, "/result", map[string]any{"output": output}, nil)
}

// Fail reports a typed drop failure.
func (c *Client) Fail(ctx context.Context, code, message string) error {
	return c.do(ctx, http.MethodPost, "/result", map[string]any{"error": resultError{Code: code, Message: message}}, nil)
}
