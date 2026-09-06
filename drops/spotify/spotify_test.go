// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package spotify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// fakeSpotify is a stand-in Spotify API. Tests read back the last request to
// assert the query Spotify is actually asked for.
type fakeSpotify struct {
	server    *httptest.Server
	lastQuery string
	lastPath  string
	lastAuth  string
}

func newFakeSpotify(t *testing.T, handler http.HandlerFunc) *fakeSpotify {
	t.Helper()
	f := &fakeSpotify{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path
		f.lastQuery = r.URL.RawQuery
		f.lastAuth = r.Header.Get("Authorization")
		handler(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// job builds a job pointed at the fake server with an injected token (the
// `token` param wins in the oauthtok resolve sequence, so no daemon lookup is
// needed in unit tests).
func (f *fakeSpotify) job(params map[string]any) core.Job {
	p := map[string]any{"token": "test-token", "base_url": f.server.URL}
	for k, v := range params {
		p[k] = v
	}
	return core.Job{ID: "j1", Params: p}
}

const followingPage = `{"artists":{
	"href":"https://api.spotify.com/v1/me/following?type=artist",
	"limit":20,
	"next":"https://api.spotify.com/v1/me/following?type=artist&after=2CIMQHirSU0MQqyYHq0eOx",
	"total":42,
	"cursors":{"after":"2CIMQHirSU0MQqyYHq0eOx"},
	"items":[
		{"id":"0OdUWJ0sBjDrqHygGUXeCF","name":"Band of Horses","genres":["indie rock"],
		 "uri":"spotify:artist:0OdUWJ0sBjDrqHygGUXeCF",
		 "external_urls":{"spotify":"https://open.spotify.com/artist/0OdUWJ0sBjDrqHygGUXeCF"},
		 "images":[{"url":"https://i.scdn.co/image/big"},{"url":"https://i.scdn.co/image/small"}]},
		{"id":"2CIMQHirSU0MQqyYHq0eOx","name":"deadmau5","genres":[],
		 "uri":"spotify:artist:2CIMQHirSU0MQqyYHq0eOx",
		 "external_urls":{"spotify":"https://open.spotify.com/artist/2CIMQHirSU0MQqyYHq0eOx"},
		 "images":[]}
	]}}`

func TestFollowedArtists_OK(t *testing.T) {
	f := newFakeSpotify(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(followingPage))
	})

	res, err := executeFollowedArtists(context.Background(), f.job(nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if f.lastPath != "/me/following" {
		t.Errorf("path = %q, want /me/following", f.lastPath)
	}
	// type=artist is not optional — Spotify rejects the call without it.
	if f.lastQuery != "limit=20&type=artist" {
		t.Errorf("query = %q, want limit=20&type=artist", f.lastQuery)
	}
	if f.lastAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", f.lastAuth)
	}

	rows, ok := res.Output["artists"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("artists = %T, want []map[string]any", res.Output["artists"].Inline)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d artists, want 2", len(rows))
	}
	if rows[0]["name"] != "Band of Horses" {
		t.Errorf("first artist = %v", rows[0]["name"])
	}
	if rows[0]["url"] != "https://open.spotify.com/artist/0OdUWJ0sBjDrqHygGUXeCF" {
		t.Errorf("url = %v", rows[0]["url"])
	}
	// The first image is the largest Spotify returns, which is the one a card
	// or an email wants.
	if rows[0]["image"] != "https://i.scdn.co/image/big" {
		t.Errorf("image = %v", rows[0]["image"])
	}
	// An artist with no artwork gets no image key rather than an empty string,
	// so a template can test for its absence.
	if _, has := rows[1]["image"]; has {
		t.Errorf("artist without images carries an image key: %v", rows[1])
	}
	if got := res.Output["next_cursor"].Inline; got != "2CIMQHirSU0MQqyYHq0eOx" {
		t.Errorf("next_cursor = %v", got)
	}
	if got := res.Output["has_more"].Inline; got != "true" {
		t.Errorf("has_more = %v, want true", got)
	}
}

func TestFollowedArtists_PagesWithAfterAndClampsLimit(t *testing.T) {
	f := newFakeSpotify(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(followingPage))
	})
	_, err := executeFollowedArtists(context.Background(),
		f.job(map[string]any{"limit": 500, "after_id": "2CIMQHirSU0MQqyYHq0eOx"}), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// 500 clamps to Spotify's cap rather than being sent through and 400ing.
	if f.lastQuery != "after=2CIMQHirSU0MQqyYHq0eOx&limit=50&type=artist" {
		t.Errorf("query = %q", f.lastQuery)
	}
}

// The 'After ID' input pin is what a paging loop wires, so it has to beat the
// param the author typed when the flow was built.
func TestFollowedArtists_InputCursorOverridesParam(t *testing.T) {
	f := newFakeSpotify(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(followingPage))
	})
	job := f.job(map[string]any{"after_id": "from-param"})
	job.Input = map[string]core.Ref{"after_id": {MIME: "text/plain", Inline: "from-wire"}}

	if _, err := executeFollowedArtists(context.Background(), job, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if f.lastQuery != "after=from-wire&limit=20&type=artist" {
		t.Errorf("query = %q, want the wired cursor", f.lastQuery)
	}
}

func TestFollowedArtists_LastPageHasNoMore(t *testing.T) {
	f := newFakeSpotify(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artists":{"items":[],"next":null,"total":0,"cursors":{}}}`))
	})
	res, err := executeFollowedArtists(context.Background(), f.job(nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, err = %+v", res.Status, res.Error)
	}
	if got := res.Output["has_more"].Inline; got != "false" {
		t.Errorf("has_more = %v, want false", got)
	}
	if got := res.Output["next_cursor"].Inline; got != "" {
		t.Errorf("next_cursor = %q, want empty", got)
	}
}

// An expired token is the failure a Spotify flow meets most, so the message
// has to carry Spotify's own words rather than a bare 401.
func TestFollowedArtists_SurfacesSpotifyErrorMessage(t *testing.T) {
	f := newFakeSpotify(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"status":401,"message":"The access token expired"}}`))
	})
	res, err := executeFollowedArtists(context.Background(), f.job(nil), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Fatal("want an error result for a 401")
	}
	if res.Error == nil || res.Error.Code != "spotify_error" {
		t.Fatalf("error = %+v, want code spotify_error", res.Error)
	}
	if want := "Spotify returned 401: The access token expired"; res.Error.Message != want {
		t.Errorf("message = %q, want %q", res.Error.Message, want)
	}
}
