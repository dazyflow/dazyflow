// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package encoding

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func okOut(t *testing.T, res core.Result) string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status = %v, error = %+v", res.Status, res.Error)
	}
	s, ok := res.Output["out"].Inline.(string)
	if !ok {
		t.Fatalf("out is %T, want string", res.Output["out"].Inline)
	}
	return s
}

func b64(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeBase64(t.Context(), core.Job{
		ID: "t", Params: params, Input: map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeBase64 error: %v", err)
	}
	return res
}

func hashJob(t *testing.T, in any, params map[string]any) core.Result {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	res, err := executeHash(t.Context(), core.Job{
		ID: "t", Params: params, Input: map[string]core.Ref{"in": {Inline: in}},
	}, nil)
	if err != nil {
		t.Fatalf("executeHash error: %v", err)
	}
	return res
}

func TestBase64_EncodeDecodeRoundTrip(t *testing.T) {
	enc := okOut(t, b64(t, "hello, world", map[string]any{"mode": "encode"}))
	if enc != "aGVsbG8sIHdvcmxk" {
		t.Errorf("encode = %q, want aGVsbG8sIHdvcmxk", enc)
	}
	dec := okOut(t, b64(t, enc, map[string]any{"mode": "decode"}))
	if dec != "hello, world" {
		t.Errorf("decode = %q, want %q", dec, "hello, world")
	}
}

func TestBase64_URLVariant(t *testing.T) {
	// 0xFB 0xFF encodes to "+/8=" standard, "-_8=" url-safe.
	in := string([]byte{0xfb, 0xff})
	std := okOut(t, b64(t, in, map[string]any{"mode": "encode"}))
	url := okOut(t, b64(t, in, map[string]any{"mode": "encode", "variant": "url"}))
	if std != "+/8=" || url != "-_8=" {
		t.Errorf("std=%q url=%q, want +/8= and -_8=", std, url)
	}
}

func TestBase64_DecodeInvalid(t *testing.T) {
	res := b64(t, "not base64!!!", map[string]any{"mode": "decode"})
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestBase64_NonStringInput(t *testing.T) {
	res := b64(t, 12345, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}

func TestBase64_BytesInput(t *testing.T) {
	got := okOut(t, b64(t, []byte("hi"), map[string]any{"mode": "encode"}))
	if got != "aGk=" {
		t.Errorf("encode(bytes) = %q, want aGk=", got)
	}
}

func TestHash_SHA256Hex(t *testing.T) {
	// Known SHA-256 of "abc".
	got := okOut(t, hashJob(t, "abc", map[string]any{"algo": "sha256"}))
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got != want {
		t.Errorf("sha256(abc) = %q, want %q", got, want)
	}
}

func TestHash_DefaultAlgoIsSHA256(t *testing.T) {
	got := okOut(t, hashJob(t, "abc", nil))
	if len(got) != 64 { // sha256 hex is 64 chars
		t.Errorf("default digest len = %d, want 64 (sha256 hex)", len(got))
	}
}

func TestHash_MD5(t *testing.T) {
	got := okOut(t, hashJob(t, "abc", map[string]any{"algo": "md5"}))
	if got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("md5(abc) = %q", got)
	}
}

func TestHash_Base64Encoding(t *testing.T) {
	got := okOut(t, hashJob(t, "abc", map[string]any{"algo": "sha256", "encoding": "base64"}))
	want := "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0="
	if got != want {
		t.Errorf("sha256(abc) b64 = %q, want %q", got, want)
	}
}

func TestHash_HMAC(t *testing.T) {
	// Known HMAC-SHA256(key="key", msg="The quick brown fox jumps over the lazy dog").
	got := okOut(t, hashJob(t, "The quick brown fox jumps over the lazy dog",
		map[string]any{"algo": "sha256", "key": "key"}))
	want := "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got != want {
		t.Errorf("hmac-sha256 = %q, want %q", got, want)
	}
}

func TestHash_BadAlgo(t *testing.T) {
	res := hashJob(t, "abc", map[string]any{"algo": "crc32"})
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status/code = %v/%v, want error/bad_param", res.Status, res.Error)
	}
}

func TestHash_NonStringInput(t *testing.T) {
	res := hashJob(t, map[string]any{"a": 1}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("status/code = %v/%v, want error/bad_input", res.Status, res.Error)
	}
}
