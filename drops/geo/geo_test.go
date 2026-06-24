package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// The network drops dial a 127.0.0.1 httptest server, so they need the same
// private-egress opt-in production gets via DAZYFLOW_ALLOW_PRIVATE_EGRESS.
func init() { hfnet.SetAllowPrivateEgress(true) }

const sampleSearch = `[{"lat":"59.3293","lon":"18.0686","display_name":"Stockholm, Sweden","address":{"city":"Stockholm","country":"Sweden"}}]`
const sampleReverse = `{"lat":"59.3293","lon":"18.0686","display_name":"Stockholm, Södermanland, Sweden","address":{"city":"Stockholm","country":"Sweden"}}`

// stubNominatim points nominatimURL at a server that records the request and
// returns the given status + body. It restores nominatimURL on cleanup.
func stubNominatim(t *testing.T, status int, body string, gotReq **http.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			*gotReq = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := nominatimURL
	nominatimURL = srv.URL
	t.Cleanup(func() { nominatimURL = prev; srv.Close() })
}

func textPin(t *testing.T, r core.Result, port string) string {
	t.Helper()
	ref, ok := r.Output[port]
	if !ok {
		t.Fatalf("missing output pin %q", port)
	}
	s, ok := ref.Inline.(string)
	if !ok {
		t.Fatalf("output pin %q is %T, want string", port, ref.Inline)
	}
	return s
}

func TestParseLatLon(t *testing.T) {
	if lat, lon, err := parseLatLon(" 59.33 , 18.07 "); err != nil || lat != 59.33 || lon != 18.07 {
		t.Fatalf("got (%v,%v,%v)", lat, lon, err)
	}
	for _, bad := range []string{"59.33", "a,b", "91,0", "0,200", "1,2,3"} {
		if _, _, err := parseLatLon(bad); err == nil {
			t.Errorf("parseLatLon(%q): want error", bad)
		}
	}
}

// --- Location (geo_location) --------------------------------------------------

func TestExecuteLocation_Point(t *testing.T) {
	// Map pin only, no Place → coordinate from the pin, no network.
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{"point": "59.3293,18.0686"}}, nil)
	if r.Status != core.StatusOK {
		t.Fatalf("status %v err %+v", r.Status, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate = %q", got)
	}
	if textPin(t, r, "place") != "" {
		t.Errorf("place should be empty for a map pin, got %q", textPin(t, r, "place"))
	}
}

func TestExecuteLocation_Neither(t *testing.T) {
	r, _ := executeLocation(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Errorf("empty: want bad_param, got %+v", r.Error)
	}
}

func TestExecuteLocation_PlaceParamOverridesPoint(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleSearch, &req)
	job := core.Job{Params: map[string]any{"point": "1.0,2.0", "place": "Stockholm"}}
	r, err := executeLocation(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("Place should override the pin, coordinate = %q", got)
	}
	if req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("geocoded query = %q", req.URL.Query().Get("q"))
	}
}

func TestExecuteLocation_PlaceInputOverridesParam(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleSearch, &req)
	job := core.Job{
		Params: map[string]any{"point": "1.0,2.0", "place": "typed"},
		Input:  map[string]core.Ref{"place": {Inline: "Stockholm"}},
	}
	if r, _ := executeLocation(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("Place input should override the typed Place param, got q=%q", req.URL.Query().Get("q"))
	}
}

// --- Reverse geocode (geo_reverse) -------------------------------------------

func TestExecuteReverse_Point(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	// Map pin (point param), no Coordinate input.
	r, err := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "59.3293,18.0686"}}, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if textPin(t, r, "place") != "Stockholm, Södermanland, Sweden" {
		t.Errorf("place = %q", textPin(t, r, "place"))
	}
	if textPin(t, r, "coordinate") != "59.3293,18.0686" {
		t.Errorf("coordinate echo = %q", textPin(t, r, "coordinate"))
	}
	if req.URL.Path != "/reverse" || req.URL.Query().Get("lat") != "59.3293" {
		t.Errorf("request = %s?%s", req.URL.Path, req.URL.RawQuery)
	}
	if _, ok := r.Output["address"]; !ok {
		t.Error("missing address pin")
	}
}

func TestExecuteReverse_CoordInputOverridesPoint(t *testing.T) {
	var req *http.Request
	stubNominatim(t, 200, sampleReverse, &req)
	job := core.Job{
		Params: map[string]any{"point": "1.0,2.0"},
		Input:  map[string]core.Ref{"coordinate": {Inline: "59.3293,18.0686"}},
	}
	if r, _ := executeReverse(context.Background(), job, nil); r.Status != core.StatusOK {
		t.Fatalf("status %v %+v", r.Status, r.Error)
	}
	if req.URL.Query().Get("lat") != "59.3293" {
		t.Errorf("Coordinate input should override the map pin, got lat=%q", req.URL.Query().Get("lat"))
	}
}

func TestExecuteReverse_Neither_NoNetwork(t *testing.T) {
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got %+v", r.Error)
	}
	r, _ = executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "not-a-point"}}, nil)
	if r.Error == nil || r.Error.Code != "bad_param" {
		t.Fatalf("bad point: want bad_param, got %+v", r.Error)
	}
}

func TestHTTPFailure_RateLimited(t *testing.T) {
	stubNominatim(t, 403, `rate limited`, nil)
	r, _ := executeReverse(context.Background(), core.Job{Params: map[string]any{"point": "1,2"}}, nil)
	if r.Error == nil || r.Error.Code != "rate_limited" {
		t.Fatalf("403 → want rate_limited, got %+v", r.Error)
	}
}
