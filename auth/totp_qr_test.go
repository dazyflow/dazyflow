// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"github.com/pquerna/otp/totp"
)

// The enrolment QR must be a real, decodable PNG an authenticator can scan.
func TestEncodeQR_DecodablePNG(t *testing.T) {
	k, err := totp.Generate(totp.GenerateOpts{Issuer: TOTPIssuer, AccountName: "qr@example.com"})
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	dataURL, err := encodeQR(k)
	if err != nil {
		t.Fatalf("encodeQR: %v", err)
	}
	b64, ok := strings.CutPrefix(dataURL, "data:image/png;base64,")
	if !ok {
		t.Fatalf("unexpected data URL prefix: %.40q", dataURL)
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != totpQRSize || b.Dy() != totpQRSize {
		t.Errorf("QR is %dx%d, want %dx%d", b.Dx(), b.Dy(), totpQRSize, totpQRSize)
	}
}
