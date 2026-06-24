package geo

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// Photon speaks GeoJSON: a FeatureCollection of Point features whose
// coordinates are [lon, lat] (the opposite of "lat,lon") and whose address is a
// flat properties bag with no Nominatim-style display_name.
const samplePhoton = `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[18.0686,59.3293]},"properties":{"name":"Stockholm","city":"Stockholm","state":"Stockholm County","country":"Sweden","countrycode":"SE","osm_id":398021,"type":"city"}}]}`

// stubPhoton points photonURL at a recording httptest server, restored on
// cleanup. Tests select the backend the way a tenant does — via the `backend`
// connection param on the job (see photonJob).
func stubPhoton(t *testing.T, status int, body string, gotReq **http.Request) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotReq != nil {
			*gotReq = r
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	prev := photonURL
	photonURL = srv.URL
	t.Cleanup(func() { photonURL = prev; srv.Close() })
}

// photonJob builds a job whose connection selects the Photon backend, merging
// any extra params.
func photonJob(extra map[string]any) core.Job {
	p := map[string]any{"backend": "photon"}
	maps.Copy(p, extra)
	return core.Job{Params: p}
}

func TestNewGeocoder_Selection(t *testing.T) {
	cases := map[string]string{
		"photon":     "Photon",
		"locationiq": "LocationIQ",
		"":           "OpenStreetMap",
		"nominatim":  "OpenStreetMap",
		"bogus":      "OpenStreetMap", // unknown → default
	}
	for name, wantLabel := range cases {
		if got := newGeocoder(name).label(); got != wantLabel {
			t.Errorf("newGeocoder(%q).label() = %q, want %q", name, got, wantLabel)
		}
	}
}

// geocoderFor honours the connection `backend` over the deployment default.
func TestGeocoderFor_BackendParam(t *testing.T) {
	if got := geocoderFor(core.Job{Params: map[string]any{"backend": "photon"}}).label(); got != "Photon" {
		t.Errorf("backend=photon → %q, want Photon", got)
	}
	if got := geocoderFor(core.Job{Params: map[string]any{}}).label(); got != "OpenStreetMap" {
		t.Errorf("no backend → %q, want OpenStreetMap (default)", got)
	}
}

func TestPhoton_ForwardViaLocation(t *testing.T) {
	var req *http.Request
	stubPhoton(t, 200, samplePhoton, &req)
	job := photonJob(map[string]any{"point": "1.0,2.0", "place": "Stockholm"})
	r, err := executeLocation(context.Background(), job, nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	// GeoJSON [lon,lat] must be flipped back into "lat,lon".
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate = %q, want 59.3293,18.0686 (lon/lat flip)", got)
	}
	if got := textPin(t, r, "place"); got != "Stockholm, Stockholm County, Sweden" {
		t.Errorf("composed place = %q", got)
	}
	if req.URL.Path != "/api" || req.URL.Query().Get("q") != "Stockholm" {
		t.Errorf("request = %s?%s, want /api?q=Stockholm", req.URL.Path, req.URL.RawQuery)
	}
}

func TestPhoton_ReverseViaReverse(t *testing.T) {
	var req *http.Request
	stubPhoton(t, 200, samplePhoton, &req)
	r, err := executeReverse(context.Background(), photonJob(map[string]any{"point": "59.3293,18.0686"}), nil)
	if err != nil || r.Status != core.StatusOK {
		t.Fatalf("status %v err %v %+v", r.Status, err, r.Error)
	}
	if got := textPin(t, r, "place"); got != "Stockholm, Stockholm County, Sweden" {
		t.Errorf("place = %q", got)
	}
	// Reverse echoes the queried coordinate, not the backend's.
	if got := textPin(t, r, "coordinate"); got != "59.3293,18.0686" {
		t.Errorf("coordinate echo = %q", got)
	}
	if req.URL.Path != "/reverse" || req.URL.Query().Get("lat") != "59.3293" || req.URL.Query().Get("lon") != "18.0686" {
		t.Errorf("request = %s?%s", req.URL.Path, req.URL.RawQuery)
	}
	if _, ok := r.Output["address"]; !ok {
		t.Error("missing address pin")
	}
}

func TestPhoton_NoMatch(t *testing.T) {
	stubPhoton(t, 200, `{"type":"FeatureCollection","features":[]}`, nil)
	r, _ := executeReverse(context.Background(), photonJob(map[string]any{"point": "0,0"}), nil)
	if r.Error == nil || r.Error.Code != "no_match" {
		t.Fatalf("empty features → want no_match, got %+v", r.Error)
	}
}

func TestPhoton_RateLimited(t *testing.T) {
	stubPhoton(t, 429, `slow down`, nil)
	r, _ := executeReverse(context.Background(), photonJob(map[string]any{"point": "1,2"}), nil)
	if r.Error == nil || r.Error.Code != "rate_limited" {
		t.Fatalf("429 → want rate_limited, got %+v", r.Error)
	}
}

func TestPhotonDisplayName(t *testing.T) {
	cases := []struct {
		props map[string]any
		want  string
	}{
		// name == city collapses; state + country append.
		{map[string]any{"name": "Stockholm", "city": "Stockholm", "state": "Stockholm County", "country": "Sweden"}, "Stockholm, Stockholm County, Sweden"},
		// street + housenumber join, then locality.
		{map[string]any{"name": "Drottninggatan", "street": "Drottninggatan", "housenumber": "5", "city": "Stockholm", "country": "Sweden"}, "Drottninggatan, Drottninggatan 5, Stockholm, Sweden"},
		// sparse properties.
		{map[string]any{"country": "Sweden"}, "Sweden"},
		// non-string values are ignored, not panicked on.
		{map[string]any{"name": "X", "osm_id": 1.0, "city": nil}, "X"},
		{map[string]any{}, ""},
	}
	for i, c := range cases {
		if got := photonDisplayName(c.props); got != c.want {
			t.Errorf("case %d: got %q, want %q", i, got, c.want)
		}
	}
}
