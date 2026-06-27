package mqtt

import (
	"context"
	"net"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestPahoPublish_ConnectError exercises the real paho-backed publish against a
// closed port: the connection is refused (or times out), so pahoPublish returns
// a connect error. This is the only reachable path through pahoPublish without a
// live broker — the publish/disconnect path needs a real MQTT server.
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

// TestPahoPublish_DefaultTimeout covers the to<=0 → 15s default branch in
// pahoPublish. The dial still fails fast against a closed port, but the branch
// taken is the fallback timeout assignment.
func TestPahoPublish_DefaultTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	// TimeoutMS 0 → the 15s fallback; the connect still fails immediately
	// (refused) so the test doesn't actually wait 15s.
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

// TestPublish_NonTextInputsRejected covers executePublish's two bad_input
// branches: a non-text value wired into the Topic or Payload port.
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

// TestPublish_BlankTopicAfterTrim covers the whitespace-only topic branch,
// which is distinct from a missing topic.
func TestPublish_BlankTopicAfterTrim(t *testing.T) {
	withFakePublish(t, nil)
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "   ", "payload": "x"}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param", res)
	}
}

// TestPublish_NegativeQoSRejected covers the lower bound of the qos range check
// (the existing test only covers qos > 2).
func TestPublish_NegativeQoSRejected(t *testing.T) {
	withFakePublish(t, nil)
	res := run(t, map[string]any{"broker": "tcp://b:1883", "topic": "t", "payload": "x", "qos": -1}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("res = %+v, want bad_param", res)
	}
}

// TestPublish_CustomClientID covers the explicit client_id branch (the existing
// test only covers the dazyflow-<jobid> default).
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

// TestBrokerHostPort_Empty covers the empty-string branches of brokerHostPort:
// an empty input and a scheme-only input with nothing after "://".
func TestBrokerHostPort_Empty(t *testing.T) {
	for _, in := range []string{"", "tcp://", "tcp:///path"} {
		if got := brokerHostPort(in); got != "" {
			t.Errorf("brokerHostPort(%q) = %q, want empty", in, got)
		}
	}
}

// TestNormalizeBroker covers the scheme passthrough and the bare-host prefix
// branches of normalizeBroker, including the empty-input early return.
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

// TestBrokerIsTLS covers each TLS-implying scheme and a plain tcp negative case.
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
