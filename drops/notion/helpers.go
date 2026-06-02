// Package notion hosts the native Notion connectors (notion_query_database,
// notion_create_page), migrated from the scripted TS drops. Token
// resolution mirrors the other connectors: an explicit `token` param wins
// (covers ${secret:NOTION_TOKEN} templating and a pasted integration
// token), otherwise the daemon's OAuth registry resolves the "notion"
// provider via the SetTokenLookup hook wired at startup.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
)

const notionVersion = "2022-06-28"

// richTextLimit is Notion's per-rich-text-object content cap.
const richTextLimit = 2000

type TokenLookup func(ctx context.Context, account string) (string, error)

var (
	tokenLookupMu sync.RWMutex
	tokenLookup   TokenLookup
)

func SetTokenLookup(fn TokenLookup) {
	tokenLookupMu.Lock()
	defer tokenLookupMu.Unlock()
	tokenLookup = fn
}

func resolveToken(ctx context.Context, job core.Job) (string, error) {
	if t, _ := params.StringOpt(job.Params, "token"); t != "" {
		return t, nil
	}
	account, _ := params.StringOpt(job.Params, "account")
	if account == "" {
		account = "default"
	}
	tokenLookupMu.RLock()
	fn := tokenLookup
	tokenLookupMu.RUnlock()
	if fn == nil {
		return "", fmt.Errorf("no Notion token: pass `token` directly or connect a Notion account via /api/v1/oauth/notion/authorize")
	}
	tok, err := fn(ctx, account)
	if err != nil {
		return "", fmt.Errorf("lookup token for account %q: %w", account, err)
	}
	if tok == "" {
		return "", fmt.Errorf("notion account %q is not connected", account)
	}
	return tok, nil
}

var (
	httpBaseMu sync.RWMutex
	httpBase   = "https://api.notion.com/v1"
)

// SetHTTPBase swaps the Notion API root (tests point it at httptest).
func SetHTTPBase(base string) {
	httpBaseMu.Lock()
	defer httpBaseMu.Unlock()
	httpBase = base
}

func currentHTTPBase() string {
	httpBaseMu.RLock()
	defer httpBaseMu.RUnlock()
	return httpBase
}

// notionDo runs one authenticated Notion API call. Returns status + body;
// the caller maps non-2xx via notionError.
func notionDo(ctx context.Context, method, url, token string, body []byte, timeoutMS int) (int, []byte, error) {
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// notionError pulls the {code,message} out of a Notion error body.
func notionError(status int, body []byte) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Message != "" {
		if e.Code != "" {
			return fmt.Sprintf("Notion %s: %s", e.Code, e.Message)
		}
		return e.Message
	}
	trunc := body
	if len(trunc) > 512 {
		trunc = trunc[:512]
	}
	return fmt.Sprintf("Notion returned %d: %s", status, string(trunc))
}
