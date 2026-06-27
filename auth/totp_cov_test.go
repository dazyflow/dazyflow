// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
)

func TestLoadTOTPKey_Cov(t *testing.T) {
	// Unset → ErrTOTPKeyMissing.
	t.Setenv(totpEnvKey, "")
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMissing) {
		t.Errorf("missing key err = %v, want ErrTOTPKeyMissing", err)
	}

	// Valid 32-byte std base64.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(key))
	got, err := LoadTOTPKey()
	if err != nil || len(got) != 32 {
		t.Errorf("valid key = %d bytes, %v", len(got), err)
	}

	// Wrong length (valid base64 but 16 bytes) → malformed.
	t.Setenv(totpEnvKey, base64.StdEncoding.EncodeToString(make([]byte, 16)))
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMalformed) {
		t.Errorf("short key err = %v, want ErrTOTPKeyMalformed", err)
	}

	// Not base64 at all → malformed.
	t.Setenv(totpEnvKey, "!!!not base64!!!")
	if _, err := LoadTOTPKey(); !errors.Is(err, ErrTOTPKeyMalformed) {
		t.Errorf("non-base64 key err = %v, want ErrTOTPKeyMalformed", err)
	}
}

func TestDisableTOTP_Cov(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "disable@example.com"
	users := newUserStoreWithUser(t, email)
	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}

	if err := DisableTOTP(ctx, users, email); err != nil {
		t.Fatalf("DisableTOTP: %v", err)
	}
	st, _ := LoadTOTPStatus(ctx, users, email)
	if st.Enabled || st.RecoveryCodesLeft != 0 {
		t.Errorf("post-disable status = %+v", st)
	}

	// Unknown user surfaces the store error.
	if err := DisableTOTP(ctx, users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("DisableTOTP(unknown) = %v, want ErrUnknownUser", err)
	}
}

func TestRegenerateRecoveryCodes_Cov(t *testing.T) {
	ctx := context.Background()
	key := testTOTPKey(t)
	const email = "regen@example.com"
	users := newUserStoreWithUser(t, email)

	// Not enrolled yet → ErrTOTPNotEnrolled.
	if _, err := RegenerateRecoveryCodes(ctx, users, email); !errors.Is(err, ErrTOTPNotEnrolled) {
		t.Errorf("regen before enrol err = %v, want ErrTOTPNotEnrolled", err)
	}
	// Unknown user → store error.
	if _, err := RegenerateRecoveryCodes(ctx, users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("regen unknown = %v, want ErrUnknownUser", err)
	}

	setup, _ := EnrolStart(ctx, users, key, email)
	if _, err := EnrolConfirm(ctx, users, key, email, codeFor(t, setup.SecretBase32)); err != nil {
		t.Fatalf("EnrolConfirm: %v", err)
	}
	codes, err := RegenerateRecoveryCodes(ctx, users, email)
	if err != nil {
		t.Fatalf("RegenerateRecoveryCodes: %v", err)
	}
	if len(codes) != totpRecoveryCodeCount {
		t.Errorf("got %d codes, want %d", len(codes), totpRecoveryCodeCount)
	}
}

func TestLoadTOTPStatus_UnknownUser(t *testing.T) {
	users := newUserStoreWithUser(t, "real@example.com")
	if _, err := LoadTOTPStatus(context.Background(), users, "ghost@example.com"); err != ErrUnknownUser {
		t.Errorf("LoadTOTPStatus(unknown) = %v, want ErrUnknownUser", err)
	}
}

func TestDecryptTOTPSecret_Corrupt(t *testing.T) {
	key := testTOTPKey(t)
	// Too-short blob → ErrTOTPSecretCorrupt.
	if _, err := decryptTOTPSecret(key, []byte("short")); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("short blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
	// Valid-length blob but garbage ciphertext → corrupt.
	blob, err := encryptTOTPSecret(key, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob[len(blob)-1] ^= 0xFF // tamper
	if _, err := decryptTOTPSecret(key, blob); !errors.Is(err, ErrTOTPSecretCorrupt) {
		t.Errorf("tampered blob err = %v, want ErrTOTPSecretCorrupt", err)
	}
}
