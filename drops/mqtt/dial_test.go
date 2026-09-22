// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mqtt

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func wsEcho(t *testing.T) *httptest.Server {
	t.Helper()
	up := websocket.Upgrader{Subprotocols: []string{"mqtt"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			// Split the echo across two messages: the stream must reassemble.
			_ = c.WriteMessage(mt, msg[:1])
			_ = c.WriteMessage(mt, msg[1:])
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ws:// brokers dial through the SSRF guard like tcp ones do.
func TestGuardedOpen_WebsocketBlockedAtDial(t *testing.T) {
	srv := wsEcho(t)
	hfnet.SetAllowPrivateEgress(false)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(true) })
	u, _ := url.Parse("ws" + strings.TrimPrefix(srv.URL, "http"))
	if c, err := guardedOpen(2*time.Second, &tls.Config{})(u, mqttlib.ClientOptions{}); err == nil {
		c.Close()
		t.Fatal("ws dial to loopback was not blocked")
	}
}

func TestGuardedOpen_WebsocketStream(t *testing.T) {
	srv := wsEcho(t)
	u, _ := url.Parse("ws" + strings.TrimPrefix(srv.URL, "http"))
	c, err := guardedOpen(2*time.Second, &tls.Config{})(u, mqttlib.ClientOptions{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q, %v", buf, err)
	}
}

func TestPublish_EgressAllowlistApplies(t *testing.T) {
	withFakePublish(t, nil)
	if err := hfnet.SetEgressAllowlist([]string{"allowed.example.com"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hfnet.SetEgressAllowlist(nil) })
	res := run(t, map[string]any{"broker": "tcp://other.example.com:1883", "topic": "t", "payload": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Errorf("res = %+v, want egress_blocked", res)
	}
}
