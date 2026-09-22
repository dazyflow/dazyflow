// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mqtt

import (
	"crypto/tls"
	"fmt"
	"io"
	stdnet "net"
	"net/url"
	"strings"
	"sync"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"
	"github.com/gorilla/websocket"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// guardedOpen replaces paho's connection opener so every broker scheme dials
// through the SSRF Control hook. paho applies SetDialer only to tcp/ssl: its
// ws/wss path builds a gorilla dialer of its own (and honours proxy env vars),
// so a ws:// broker resolving to a private address was never checked at dial.
func guardedOpen(timeout time.Duration, tlsc *tls.Config) mqttlib.OpenConnectionFunc {
	dialer := &stdnet.Dialer{Timeout: timeout, Control: hfnet.SSRFDialControl()}
	return func(uri *url.URL, _ mqttlib.ClientOptions) (stdnet.Conn, error) {
		switch scheme := strings.ToLower(uri.Scheme); scheme {
		case "mqtt", "tcp":
			return dialer.Dial("tcp", uri.Host)
		case "ssl", "tls", "mqtts", "mqtt+ssl", "tcps":
			return tls.DialWithDialer(dialer, "tcp", uri.Host, tlsc)
		case "ws", "wss":
			wd := &websocket.Dialer{
				NetDialContext:   dialer.DialContext,
				HandshakeTimeout: timeout,
				Subprotocols:     []string{"mqtt"},
			}
			if scheme == "wss" {
				wd.TLSClientConfig = tlsc
			}
			u := *uri
			u.User = nil // gorilla rejects userinfo; credentials go in CONNECT
			ws, resp, err := wd.Dial(u.String(), nil)
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			if err != nil {
				return nil, err
			}
			return &wsConn{Conn: ws}, nil
		default:
			return nil, fmt.Errorf("broker scheme %q not supported (use tcp://, ssl://, ws:// or wss://)", uri.Scheme)
		}
	}
}

// wsConn presents a websocket as the byte stream paho expects: MQTT packets
// ride in binary messages, which may split or join packets arbitrarily.
type wsConn struct {
	*websocket.Conn
	rmu, wmu sync.Mutex
	r        io.Reader
}

func (c *wsConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if c.r == nil {
			_, r, err := c.NextReader()
			if err != nil {
				return 0, err
			}
			c.r = r
		}
		n, err := c.r.Read(p)
		if err == io.EOF {
			c.r = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
