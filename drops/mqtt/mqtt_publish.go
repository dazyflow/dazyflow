package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "mqtt_publish",
			Version:     "1.0",
			Label:       "MQTT",
			Subtitle:    "Publish",
			Summary:     "Publish a message to an MQTT topic.",
			Description: "Publish a message to a topic on an MQTT broker. The topic and payload can be typed on the step or wired in from upstream (the matching input overrides the param). Broker is a tcp:// or ssl:// address (a bare host:port defaults to tcp://…:1883). Username/Password default to the MQTT_USERNAME/MQTT_PASSWORD secrets. Private-network brokers are blocked unless the operator allows private egress.",
			Integration: "MQTT",
			Category:    "network",
			Icon:        "radio",
			BrandLogo:   "/brands/mqtt.svg",
			Color:       "#660066",
			Provider:    "internal",
			Tags:        []string{"mqtt", "publish", "iot", "message", "pubsub"},
			Examples: []core.ParamsExample{
				{Title: "Publish a sensor command", Params: json.RawMessage(`{"broker":"tcp://broker.example.com:1883","topic":"home/livingroom/light","payload":"ON","qos":1}`)},
				{Title: "Retained status over TLS", Params: json.RawMessage(`{"broker":"ssl://broker.example.com:8883","topic":"devices/door/status","payload":"open","retain":true}`)},
			},
			RequiresConnections: []core.ConnectionRequirement{
				{Kind: "secret", Name: "MQTT_USERNAME", Note: "MQTT broker username (optional — omit for anonymous brokers)."},
				{Kind: "secret", Name: "MQTT_PASSWORD", Note: "MQTT broker password (optional)."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "topic", Label: "Topic", MIME: []string{"text/plain"}},
				{Port: "payload", Label: "Payload", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "meta", Label: "Details", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"broker":{"type":"string","title":"Broker","examples":["tcp://broker.example.com:1883","ssl://broker.example.com:8883"],"description":"Broker address. A bare host:port defaults to tcp://…:1883; ssl:// uses TLS."},
					"topic":{"type":"string","title":"Topic","examples":["home/livingroom/light"],"description":"Topic to publish to. Overridden by the 'Topic' input."},
					"payload":{"type":"string","title":"Payload","description":"Message body. Overridden by the 'Payload' input."},
					"qos":{"type":"integer","title":"QoS","enum":[0,1,2],"default":0,"description":"Delivery guarantee: 0 at-most-once, 1 at-least-once, 2 exactly-once."},
					"retain":{"type":"boolean","title":"Retain","default":false,"description":"Broker keeps this as the topic's last known message for new subscribers."},
					"username":{"type":"string","title":"Username","default":"${secret.MQTT_USERNAME}","x_advanced":true,"description":"Broker username. Defaults to the MQTT_USERNAME secret; leave blank for anonymous brokers."},
					"password":{"type":"string","title":"Password","default":"${secret.MQTT_PASSWORD}","x_advanced":true,"description":"Broker password. Defaults to the MQTT_PASSWORD secret."},
					"client_id":{"type":"string","title":"Client ID","x_advanced":true,"description":"MQTT client id. Defaults to a dazyflow-derived id."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for connect + publish, in milliseconds."}
				},
				"required":["broker","topic","payload"]
			}`),
			// A publish is a side effect with no dedup key, and each retry
			// reconnects (a fresh session), so MQTT QoS can't suppress a
			// duplicate. Never auto-retry — matches the other send-drops.
			Idempotent:  false,
			RetryPolicy: core.RetryNever,
		},
		Execute: executePublish,
	})
}

func executePublish(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	broker := normalizeBroker(params.StringDefault(job.Params, "broker", ""))
	if broker == "" {
		return params.Err(job, "bad_param", "'broker' is required (e.g. tcp://broker.example.com:1883)"), nil
	}
	topic, ok := params.TextInputOr(job, "topic", params.StringDefault(job.Params, "topic", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Topic' input must be text"), nil
	}
	if strings.TrimSpace(topic) == "" {
		return params.Err(job, "bad_param", "'topic' is required — set it or wire the 'Topic' input"), nil
	}
	payload, ok := params.TextInputOr(job, "payload", params.StringDefault(job.Params, "payload", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Payload' input must be text"), nil
	}

	qosN := params.IntDefault(job.Params, "qos", 0)
	if qosN < 0 || qosN > 2 {
		return params.Err(job, "bad_param", fmt.Sprintf("'qos' must be 0, 1 or 2 (got %d)", qosN)), nil
	}

	// Pre-dial SSRF check for a clean error; the dialer's Control hook is the
	// rebinding-proof backstop on the actual connection.
	if err := hfnet.CheckDialHost(brokerHostPort(broker)); err != nil {
		return params.Err(job, "egress_blocked", err.Error()), nil
	}

	clientID := strings.TrimSpace(params.StringDefault(job.Params, "client_id", ""))
	if clientID == "" {
		clientID = "dazyflow-" + job.ID
	}

	cfg := publishConfig{
		Broker:    broker,
		ClientID:  clientID,
		Username:  params.StringDefault(job.Params, "username", ""),
		Password:  params.StringDefault(job.Params, "password", ""),
		Topic:     topic,
		Payload:   []byte(payload),
		QoS:       byte(qosN),
		Retain:    params.BoolDefault(job.Params, "retain", false),
		TLS:       brokerIsTLS(broker),
		TimeoutMS: params.IntDefault(job.Params, "timeout_ms", 15000),
	}

	if err := publishFn(ctx, cfg); err != nil {
		return params.Err(job, "mqtt_error", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"meta": {MIME: "application/json", Inline: map[string]any{
				"broker": broker,
				"topic":  topic,
				"qos":    qosN,
				"retain": cfg.Retain,
				"bytes":  len(cfg.Payload),
			}},
		},
	}, nil
}
