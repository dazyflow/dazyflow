// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package encoding

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "hash",
			Version:     "1.0",
			Label:       "Hash",
			Subtitle:    "Checksum or HMAC a value",
			Icon:        "fingerprint",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"hash", "checksum", "hmac", "sha256", "md5", "signature", "transform"},
			Description: "Compute a hash of the input — a plain digest for a checksum/dedupe key, or a keyed HMAC for verifying (or signing) a webhook. 'algo' picks sha256 (default), sha512, sha1, or md5. Set 'key' to switch from a plain hash to HMAC with that secret (connect a ${secret.…} in). 'encoding' renders the digest as hex (default) or base64. sha1/md5 are for compatibility and checksums only — use sha256+ for anything security-sensitive.",
			Summary:     "Hash or HMAC the input value; output hex or Base64 (sha256/sha512/sha1/md5).",
			Examples: []core.ParamsExample{
				{
					Title:  "SHA-256 checksum (hex)",
					Params: json.RawMessage(`{"algo":"sha256"}`),
				},
				{
					Title:  "HMAC-SHA256 for webhook signing, Base64",
					Params: json.RawMessage(`{"algo":"sha256","key":"${secret.WEBHOOK_SIGNING_KEY}","encoding":"base64"}`),
					Notes:  "With 'key' set the digest is a keyed HMAC — the same value the sender computes to sign a payload.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Value", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Digest", MIME: []string{"text/plain"}, Example: json.RawMessage(`"9f2ab7c4d1e0b7a38f124471e0b7a39f2ab7c4d1e0b7a38f124471e0b7a39f2a"`)},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"algo":{"type":"string","enum":["sha256","sha512","sha1","md5"],"enumNames":["SHA-256","SHA-512","SHA-1","MD5"],"default":"sha256","title":"Algorithm","description":"Hash algorithm. Use sha256 or stronger for security; sha1/md5 are for checksums/compatibility only."},
					"key":{"type":"string","title":"HMAC key","description":"When set, compute a keyed HMAC with this secret instead of a plain hash. Connect a ${secret.NAME} in rather than pasting the key."},
					"encoding":{"type":"string","enum":["hex","base64"],"enumNames":["Hex","Base64"],"default":"hex","title":"Output encoding","description":"Render the digest as lowercase hex (default) or standard Base64."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeHash,
	})
}

func executeHash(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	text, ok := stringInput(job.Input["in"])
	if !ok {
		return params.Err(job, "bad_input", "input port 'in' must be text"), nil
	}

	newHash, ok := hashFuncs[params.StringDefault(job.Params, "algo", "sha256")]
	if !ok {
		return params.Err(job, "bad_param", "algo must be one of sha256, sha512, sha1, md5"), nil
	}

	var h hash.Hash
	if key := params.StringDefault(job.Params, "key", ""); key != "" {
		h = hmac.New(newHash, []byte(key))
	} else {
		h = newHash()
	}
	h.Write([]byte(text))
	sum := h.Sum(nil)

	switch params.StringDefault(job.Params, "encoding", "hex") {
	case "hex":
		return textResult(job, hex.EncodeToString(sum)), nil
	case "base64":
		return textResult(job, base64.StdEncoding.EncodeToString(sum)), nil
	default:
		return params.Err(job, "bad_param", "encoding must be \"hex\" or \"base64\""), nil
	}
}

// hashFuncs maps the algo param to its constructor. hmac.New takes exactly
// this func() hash.Hash shape, so plain and keyed hashing share one table.
var hashFuncs = map[string]func() hash.Hash{
	"sha256": sha256.New,
	"sha512": sha512.New,
	"sha1":   sha1.New,
	"md5":    md5.New,
}
