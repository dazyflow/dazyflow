// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package mqtt

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

func TestMain(m *testing.M) {
	hfnet.SetAllowPrivateEgress(true)
	os.Exit(m.Run())
}

func withFakePublish(t *testing.T, err error) *publishConfig {
	t.Helper()
	captured := &publishConfig{}
	orig := publishFn
	publishFn = func(_ context.Context, cfg publishConfig) error {
		*captured = cfg
		return err
	}
	t.Cleanup(func() { publishFn = orig })
	return captured
}

func run(t *testing.T, params map[string]any, input map[string]core.Ref) core.Result {
	t.Helper()
	res, err := executePublish(context.Background(), core.Job{ID: "job-1", Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute err: %v", err)
	}
	return res
}

func TestPublish_SuccessAndConfig(t *testing.T) {
	cap := withFakePublish(t, nil)
	res := run(t, map[string]any{
		"broker":  "tcp://broker.example.com:1883",
		"topic":   "home/light",
		"payload": "ON",
		"qos":     2,
		"retain":  true,
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("res = %+v", res)
	}
	if cap.Broker != "tcp://broker.example.com:1883" || cap.Topic != "home/light" || string(cap.Payload) != "ON" {
		t.Errorf("cfg = %+v", cap)
	}
	if cap.QoS != 2 || !cap.Retain {
		t.Errorf("qos/retain = %d/%v", cap.QoS, cap.Retain)
	}
	if cap.ClientID != "dazyflow-job-1" {
		t.Errorf("client_id = %q", cap.ClientID)
	}
	meta := res.Output["meta"].Inline.(map[string]any)
	if meta["bytes"].(int) != 2 || meta["topic"] != "home/light" {
		t.Errorf("meta = %+v", meta)
	}
}

func TestPublish_BareHostNormalizedToTCP(t *testing.T) {
	cap := withFakePublish(t, nil)
	run(t, map[string]any{"broker": "broker.example.com:1883", "topic": "t", "payload": "x"}, nil)
	if cap.Broker != "tcp://broker.example.com:1883" {
		t.Errorf("broker = %q, want tcp:// prefix", cap.Broker)
	}
	if cap.TLS {
		t.Errorf("plain tcp broker must not be TLS")
	}
}

func TestPublish_SSLSchemeEnablesTLS(t *testing.T) {
	cap := withFakePublish(t, nil)
	run(t, map[string]any{"broker": "ssl://broker.example.com:8883", "topic": "t", "payload": "x"}, nil)
	if !cap.TLS {
		t.Errorf("ssl:// broker should enable TLS: %+v", cap)
	}
}

func TestPublish_InputPortsOverrideParams(t *testing.T) {
	cap := withFakePublish(t, nil)
	run(t, map[string]any{"broker": "tcp://b:1883", "topic": "typed", "payload": "typed"},
		map[string]core.Ref{
			"topic":   {Inline: "wired/topic"},
			"payload": {Inline: "wired payload"},
		})
	if cap.Topic != "wired/topic" || string(cap.Payload) != "wired payload" {
		t.Errorf("wired values lost: topic=%q payload=%q", cap.Topic, cap.Payload)
	}
}

func TestPublish_Validation(t *testing.T) {
	withFakePublish(t, nil)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"no broker", map[string]any{"topic": "t", "payload": "x"}},
		{"no topic", map[string]any{"broker": "tcp://b:1883", "payload": "x"}},
		{"bad qos", map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": "x", "qos": 3}},
	}
	for _, c := range cases {
		res := run(t, c.params, nil)
		if res.Status != core.StatusError || res.Error.Code != "bad_param" {
			t.Errorf("%s: res = %+v", c.name, res)
		}
	}
}

func TestPublish_EmptyPayloadAllowed(t *testing.T) {
	cap := withFakePublish(t, nil)
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": ""}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("empty payload should publish: %+v", res)
	}
	if len(cap.Payload) != 0 {
		t.Errorf("payload = %q, want empty", cap.Payload)
	}
}

func TestPublish_BrokerErrorSurfaces(t *testing.T) {
	withFakePublish(t, errors.New("connection refused"))
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "mqtt_error" {
		t.Fatalf("res = %+v", res)
	}
	if res.Error.Message != "connection refused" {
		t.Errorf("error = %q", res.Error.Message)
	}
}

func TestPublish_EgressBlocksPrivateBroker(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	called := false
	orig := publishFn
	publishFn = func(_ context.Context, _ publishConfig) error { called = true; return nil }
	defer func() { publishFn = orig }()

	res := run(t, map[string]any{"broker": "tcp://127.0.0.1:1883", "topic": "t", "payload": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "egress_blocked" {
		t.Fatalf("res = %+v", res)
	}
	if called {
		t.Errorf("publish must not run when the broker is egress-blocked")
	}
}

func TestBrokerHostPort(t *testing.T) {
	cases := map[string]string{
		"tcp://host:1883":      "host:1883",
		"tcp://host":           "host:1883", // default port added
		"ssl://h.example:8883": "h.example:8883",
		"ws://host:9001/mqtt":  "host:9001", // path stripped
	}
	for in, want := range cases {
		if got := brokerHostPort(in); got != want {
			t.Errorf("brokerHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPahoPublish_ConnectError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // closed → connect refused

	err = pahoPublish(context.Background(), publishConfig{
		Broker:    "tcp://" + addr,
		ClientID:  "dazyflow-test",
		Topic:     "t",
		Payload:   []byte("x"),
		TimeoutMS: 1500,
	})
	if err == nil {
		t.Fatal("expected a connect error against a closed port")
	}
}

func TestPahoPublish_DefaultTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	err := pahoPublish(context.Background(), publishConfig{
		Broker:   "tcp://" + addr,
		ClientID: "dazyflow-test2",
		Topic:    "t",
		Payload:  []byte("x"),
	})
	if err == nil {
		t.Fatal("expected a connect error")
	}
}

func TestPublish_NonTextInputsRejected(t *testing.T) {
	withFakePublish(t, nil)
	cases := []struct {
		name string
		port string
	}{
		{"non-text topic", "topic"},
		{"non-text payload", "payload"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": "x"},
				map[string]core.Ref{c.port: {Inline: map[string]any{"oops": true}}})
			if res.Status != core.StatusError || res.Error.Code != "bad_input" {
				t.Fatalf("res = %+v, want bad_input", res)
			}
		})
	}
}

func TestPublish_BlankTopicAfterTrim(t *testing.T) {
	withFakePublish(t, nil)
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "   ", "payload": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param", res)
	}
}

func TestPublish_NegativeQoSRejected(t *testing.T) {
	withFakePublish(t, nil)
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": "x", "qos": -1}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param", res)
	}
}

func TestPublish_CustomClientID(t *testing.T) {
	cap := withFakePublish(t, nil)
	run(t, map[string]any{
		"broker": "tcp://b:1883", "topic": "t", "payload": "x",
		"client_id": "my-client", "username": "u", "password": "p",
	}, nil)
	if cap.ClientID != "my-client" {
		t.Errorf("client_id = %q, want my-client", cap.ClientID)
	}
	if cap.Username != "u" || cap.Password != "p" {
		t.Errorf("creds = %q/%q", cap.Username, cap.Password)
	}
}

func TestBrokerHostPort_Empty(t *testing.T) {
	for _, in := range []string{"", "tcp://", "tcp:///path"} {
		if got := brokerHostPort(in); got != "" {
			t.Errorf("brokerHostPort(%q) = %q, want empty", in, got)
		}
	}
}

func TestNormalizeBroker(t *testing.T) {
	cases := map[string]string{
		"":                      "",
		"  ":                    "",
		"host:1883":             "tcp://host:1883",
		"tcp://host:1883":       "tcp://host:1883",
		"ssl://host:8883":       "ssl://host:8883",
		"  ws://host:9001/mqtt": "ws://host:9001/mqtt",
	}
	for in, want := range cases {
		if got := normalizeBroker(in); got != want {
			t.Errorf("normalizeBroker(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBrokerIsTLS(t *testing.T) {
	cases := map[string]bool{
		"tcp://h:1883": false,
		"ssl://h:8883": true,
		"tls://h:8883": true,
		"wss://h:443":  true,
		"ws://h:9001":  false,
	}
	for in, want := range cases {
		if got := brokerIsTLS(in); got != want {
			t.Errorf("brokerIsTLS(%q) = %v, want %v", in, got, want)
		}
	}
}
