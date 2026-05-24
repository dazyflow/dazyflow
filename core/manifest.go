package core

import "encoding/json"

type ExecutionModel string

const (
	ExecutionBatch   ExecutionModel = "batch"
	ExecutionStream  ExecutionModel = "stream"
	ExecutionTrigger ExecutionModel = "trigger"
)

type ProcessModel string

const (
	ProcessSpawnPerJob   ProcessModel = "spawn_per_job"
	ProcessLongLived     ProcessModel = "long_lived"
	ProcessPreRegistered ProcessModel = "pre_registered"
)

type RetryPolicy string

const (
	RetryNever              RetryPolicy = "never"
	RetryExponentialBackoff RetryPolicy = "exponential_backoff"
)

type Port struct {
	Port     string   `json:"port"`
	MIME     []string `json:"mime"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	Variadic bool     `json:"variadic"`
	Min      *int     `json:"min,omitempty"`
	Max      *int     `json:"max,omitempty"`
}

type Manifest struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Label          string          `json:"label"`
	Color          string          `json:"color"`
	ExecutionModel ExecutionModel  `json:"execution_model"`
	ProcessModel   ProcessModel    `json:"process_model"`
	Inputs         []Port          `json:"inputs"`
	Outputs        []Port          `json:"outputs"`
	ParamsSchema   json.RawMessage `json:"params_schema"`
	Idempotent     bool            `json:"idempotent"`
	RetryPolicy    RetryPolicy     `json:"retry_policy"`
	CompatibleWith []string        `json:"compatible_with"`
}

func (m Manifest) Input(name string) (Port, bool) {
	for _, p := range m.Inputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}

func (m Manifest) Output(name string) (Port, bool) {
	for _, p := range m.Outputs {
		if p.Port == name {
			return p, true
		}
	}
	return Port{}, false
}
