// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mqtt hosts the native MQTT connector (mqtt_publish). It speaks the
// MQTT protocol via the standard eclipse/paho.mqtt.golang client — MQTT is a
// TCP pub/sub protocol, not HTTP, so unlike the other connectors it needs a
// real protocol client rather than the shared net HTTP helpers.
//
// Broker connections are SSRF-guarded the same way HTTP egress is: the dialer's
// Control hook runs drops/net's SSRFDialControl on the resolved address (so a
// broker that resolves to a loopback/private/link-local host is refused unless
// the operator opted into private egress), and Execute additionally pre-checks
// the host with CheckDialHost for a clean error before dialing.
package mqtt

import (
	"context"
	"crypto/tls"
	"fmt"
	stdnet "net"
	"strings"
	"time"

	mqttlib "github.com/eclipse/paho.mqtt.golang"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// publishConfig is the resolved set of values one publish needs — the seam
// between the drop's Execute (which validates/resolves params) and the broker
// client (which tests replace).
type publishConfig struct {
	Broker    string
	ClientID  string
	Username  string
	Password  string
	Topic     string
	Payload   []byte
	QoS       byte
	Retain    bool
	TLS       bool
	TimeoutMS int
}

// publishFn is the broker-publish seam. It defaults to the real paho-backed
// implementation; tests swap it to capture the config without a live broker.
var publishFn = pahoPublish

// normalizeBroker ensures the broker address carries a scheme paho understands.
// A bare "host:1883" becomes "tcp://host:1883"; an explicit tcp/ssl/ws/wss
// scheme passes through unchanged.
func normalizeBroker(broker string) string {
	broker = strings.TrimSpace(broker)
	if broker == "" {
		return ""
	}
	if !strings.Contains(broker, "://") {
		return "tcp://" + broker
	}
	return broker
}

// brokerIsTLS reports whether the broker scheme implies TLS (ssl/tls/wss).
func brokerIsTLS(broker string) bool {
	lower := strings.ToLower(broker)
	return strings.HasPrefix(lower, "ssl://") || strings.HasPrefix(lower, "tls://") || strings.HasPrefix(lower, "wss://")
}

// brokerHostPort extracts the "host:port" from a normalized broker URL for the
// pre-dial SSRF check, defaulting the port to MQTT's 1883 when absent.
func brokerHostPort(broker string) string {
	s := broker
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Strip any path (ws/wss brokers may carry one).
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return ""
	}
	if _, _, err := stdnet.SplitHostPort(s); err != nil {
		return stdnet.JoinHostPort(s, "1883")
	}
	return s
}

// pahoPublish connects to the broker, publishes one message, and disconnects.
// The dialer's Control hook applies the SSRF policy on the resolved address.
func pahoPublish(_ context.Context, cfg publishConfig) error {
	to := time.Duration(cfg.TimeoutMS) * time.Millisecond
	if to <= 0 {
		to = 15 * time.Second
	}

	opts := mqttlib.NewClientOptions()
	opts.AddBroker(cfg.Broker)
	opts.SetClientID(cfg.ClientID)
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}
	opts.SetConnectTimeout(to)
	opts.SetAutoReconnect(false)
	// SSRF guard on the actual dial (resists DNS rebinding): the Control hook
	// runs on the resolved address. nil when the operator allowed private
	// egress, which leaves the dialer unrestricted.
	opts.SetDialer(&stdnet.Dialer{Timeout: to, Control: hfnet.SSRFDialControl()})
	if cfg.TLS {
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12})
	}

	client := mqttlib.NewClient(opts)
	tok := client.Connect()
	if !tok.WaitTimeout(to) {
		return fmt.Errorf("connect to %s timed out after %s", cfg.Broker, to)
	}
	if err := tok.Error(); err != nil {
		return fmt.Errorf("connect to %s: %w", cfg.Broker, err)
	}
	defer client.Disconnect(250)

	pt := client.Publish(cfg.Topic, cfg.QoS, cfg.Retain, cfg.Payload)
	if !pt.WaitTimeout(to) {
		return fmt.Errorf("publish to %q timed out after %s", cfg.Topic, to)
	}
	return pt.Error()
}
