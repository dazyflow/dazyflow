// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/crypto/bcrypt"
)

const (
	totpPeriodSeconds = 30
	totpSkewSteps     = 1
)

// Returns the consumed step, so the caller can refuse a replay of it.
func validateTOTPStep(code, secret string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, false
	}
	opts := totp.ValidateOpts{
		Period:    totpPeriodSeconds,
		Skew:      totpSkewSteps,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	}
	cur := now.Unix() / totpPeriodSeconds
	matched := int64(0)
	ok := false
	for d := -int64(totpSkewSteps); d <= int64(totpSkewSteps); d++ {
		step := cur + d
		if step < 0 {
			continue
		}
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriodSeconds, 0), opts)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 && !ok {
			matched = step
			ok = true
		}
	}
	return matched, ok
}

// The secret is stored encrypted, the recovery codes as bcrypt hashes, so
// neither is recoverable from the row.

// Bounds the bridge token between the two sign-in legs.
const TOTPChallengeTTL = 5 * time.Minute

const TOTPIssuer = "Dazyflow"

const totpRecoveryCodeCount = 10

const totpRecoveryCodeAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

const totpEnvKey = "DAZYFLOW_TOTP_KEY"

var (
	ErrTOTPKeyMissing      = errors.New("DAZYFLOW_TOTP_KEY is not configured")
	ErrTOTPKeyMalformed    = errors.New("DAZYFLOW_TOTP_KEY is malformed (need 32 bytes base64)")
	ErrTOTPNotEnrolled     = errors.New("totp not enrolled")
	ErrTOTPAlreadyEnrolled = errors.New("totp already enrolled")
	ErrTOTPInvalid         = errors.New("invalid totp code")
	ErrChallengeUnknown    = errors.New("totp challenge unknown")
	ErrChallengeExpired    = errors.New("totp challenge expired")
	ErrRecoveryCodeInvalid = errors.New("recovery code invalid")
	ErrTOTPSecretCorrupt   = errors.New("totp secret could not be decrypted")
)

func LoadTOTPKey() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(totpEnvKey))
	if raw == "" {
		return nil, ErrTOTPKeyMissing
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
	} {
		if b, err := dec(raw); err == nil {
			if len(b) != 32 {
				return nil, fmt.Errorf("%w: got %d bytes, want 32", ErrTOTPKeyMalformed, len(b))
			}
			return b, nil
		}
	}
	return nil, ErrTOTPKeyMalformed
}

// AES-256-GCM under the deployment key, so a leaked row yields no secret.
func encryptTOTPSecret(key []byte, plaintextBase32 string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, []byte(plaintextBase32), nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return out, nil
}

func decryptTOTPSecret(key, blob []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", ErrTOTPSecretCorrupt
	}
	nonce := blob[:gcm.NonceSize()]
	ct := blob[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTOTPSecretCorrupt, err)
	}
	return string(pt), nil
}

type TOTPSetup struct {
	OTPAuthURL   string
	SecretBase32 string
	QRPNGDataURL string
}

func encodeQR(otpauthURL string) (string, error) {
	png, err := qrcode.Encode(otpauthURL, qrcode.Medium, 256)
	if err != nil {
		return "", fmt.Errorf("qr encode: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func EnrolStart(ctx context.Context, store UserStore, key []byte, email string) (TOTPSetup, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return TOTPSetup{}, err
	}
	if u.TOTPEnabled {
		return TOTPSetup{}, ErrTOTPAlreadyEnrolled
	}
	k, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: u.Email,
	})
	if err != nil {
		return TOTPSetup{}, err
	}
	enc, err := encryptTOTPSecret(key, k.Secret())
	if err != nil {
		return TOTPSetup{}, err
	}
	u.TOTPSecretEnc = enc
	u.TOTPEnabled = false
	u.TOTPEnrolledAt = nil
	if err := store.PutUser(ctx, u); err != nil {
		return TOTPSetup{}, err
	}
	otpauth := k.URL()
	qr, err := encodeQR(otpauth)
	if err != nil {
		qr = ""
	}
	return TOTPSetup{
		OTPAuthURL:   otpauth,
		SecretBase32: k.Secret(),
		QRPNGDataURL: qr,
	}, nil
}

// Only a verified code promotes the pending secret to the live one.
func EnrolConfirm(ctx context.Context, store UserStore, key []byte, email, code string) ([]string, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if u.TOTPEnabled {
		return nil, ErrTOTPAlreadyEnrolled
	}
	if len(u.TOTPSecretEnc) == 0 {
		return nil, ErrTOTPNotEnrolled
	}
	secret, err := decryptTOTPSecret(key, u.TOTPSecretEnc)
	if err != nil {
		return nil, err
	}
	if _, ok := validateTOTPStep(code, secret, time.Now()); !ok {
		return nil, ErrTOTPInvalid
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	u.TOTPEnabled = true
	u.TOTPEnrolledAt = &now
	u.RecoveryCodeHashes = hashes
	// Deliberately does NOT burn the enrolment code's step: the user is about to sign
	// in with the very next one, and burning it would reject their first real login.
	if err := store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	return codes, nil
}

func DisableTOTP(ctx context.Context, store UserStore, email string) error {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return err
	}
	u.TOTPSecretEnc = nil
	u.TOTPEnabled = false
	u.TOTPEnrolledAt = nil
	u.RecoveryCodeHashes = nil
	return store.PutUser(ctx, u)
}

func RegenerateRecoveryCodes(ctx context.Context, store UserStore, email string) ([]string, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if !u.TOTPEnabled {
		return nil, ErrTOTPNotEnrolled
	}
	codes, hashes, err := generateRecoveryCodes()
	if err != nil {
		return nil, err
	}
	u.RecoveryCodeHashes = hashes
	if err := store.PutUser(ctx, u); err != nil {
		return nil, err
	}
	return codes, nil
}

type TOTPStatus struct {
	Enabled           bool
	EnrolledAt        *time.Time
	RecoveryCodesLeft int
}

func LoadTOTPStatus(ctx context.Context, store UserStore, email string) (TOTPStatus, error) {
	u, err := store.GetByEmail(ctx, email)
	if err != nil {
		return TOTPStatus{}, err
	}
	return TOTPStatus{
		Enabled:           u.TOTPEnabled,
		EnrolledAt:        u.TOTPEnrolledAt,
		RecoveryCodesLeft: len(u.RecoveryCodeHashes),
	}, nil
}

type Factor string

const (
	FactorTOTP         Factor = "totp"
	FactorRecoveryCode Factor = "recovery_code"
)

// Caps brute force against a single challenge token.
const maxTOTPChallengeAttempts = 5

// Bridges the two legs; holding it is not itself an authentication.
type TOTPChallenge struct {
	Email     string
	ExpiresAt time.Time
	Attempts  int
	Tenant    string
	Workspace string
	Roles     []core.Role
}

type TOTPChallengeStore interface {
	Put(ctx context.Context, token string, c TOTPChallenge) error
	Get(ctx context.Context, token string) (TOTPChallenge, error)
	Delete(ctx context.Context, token string) error
	// Atomic, so concurrent wrong guesses cannot all read the same count.
	IncrAttempts(ctx context.Context, token string) (int, error)
}

type MemTOTPChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]TOTPChallenge
}

func NewMemTOTPChallengeStore() *MemTOTPChallengeStore {
	return &MemTOTPChallengeStore{challenges: make(map[string]TOTPChallenge)}
}

func (s *MemTOTPChallengeStore) Put(_ context.Context, token string, c TOTPChallenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	s.challenges[token] = c
	return nil
}

func (s *MemTOTPChallengeStore) Get(_ context.Context, token string) (TOTPChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[token]
	if !ok {
		return TOTPChallenge{}, ErrChallengeUnknown
	}
	return c, nil
}

func (s *MemTOTPChallengeStore) Delete(_ context.Context, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.challenges, token)
	return nil
}

func (s *MemTOTPChallengeStore) IncrAttempts(_ context.Context, token string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[token]
	if !ok {
		return 0, ErrChallengeUnknown
	}
	c.Attempts++
	s.challenges[token] = c
	return c.Attempts, nil
}

func (s *MemTOTPChallengeStore) sweepLocked() {
	now := time.Now()
	for tok, c := range s.challenges {
		if now.After(c.ExpiresAt) {
			delete(s.challenges, tok)
		}
	}
}

func IssueTOTPChallenge(ctx context.Context, store TOTPChallengeStore, email string) (string, error) {
	return IssueTOTPChallengeWithOrg(ctx, store, email, "", "", nil)
}

func IssueTOTPChallengeWithOrg(ctx context.Context, store TOTPChallengeStore, email, tenant, workspace string, roles []core.Role) (string, error) {
	tok, err := newChallengeToken()
	if err != nil {
		return "", err
	}
	c := TOTPChallenge{
		Email:     email,
		ExpiresAt: time.Now().Add(TOTPChallengeTTL),
		Tenant:    tenant,
		Workspace: workspace,
		Roles:     roles,
	}
	if err := store.Put(ctx, tok, c); err != nil {
		return "", err
	}
	return tok, nil
}

type TOTPChallengeResult struct {
	User   User
	Factor Factor
}

// Single-use: a consumed token must not be replayable.
func ConsumeTOTPChallenge(ctx context.Context, challenges TOTPChallengeStore, users UserStore, key []byte, token, code, recoveryCode string) (TOTPChallengeResult, error) {
	if token == "" {
		return TOTPChallengeResult{}, ErrChallengeUnknown
	}
	c, err := challenges.Get(ctx, token)
	if err != nil {
		return TOTPChallengeResult{}, err
	}
	if time.Now().After(c.ExpiresAt) {
		_ = challenges.Delete(ctx, token)
		return TOTPChallengeResult{}, ErrChallengeExpired
	}
	u, err := users.GetByEmail(ctx, c.Email)
	if err != nil {
		return TOTPChallengeResult{}, err
	}
	if !u.TOTPEnabled || len(u.TOTPSecretEnc) == 0 {
		return TOTPChallengeResult{}, ErrTOTPNotEnrolled
	}

	// Each wrong guess costs an attempt, and the challenge dies at the cap.
	recordWrongGuess := func() {
		n, err := challenges.IncrAttempts(ctx, token)
		if err == nil && n >= maxTOTPChallengeAttempts {
			_ = challenges.Delete(ctx, token)
		}
	}

	var factor Factor
	switch {
	case strings.TrimSpace(code) != "":
		secret, derr := decryptTOTPSecret(key, u.TOTPSecretEnc)
		if derr != nil {
			return TOTPChallengeResult{}, derr
		}
		step, ok := validateTOTPStep(code, secret, time.Now())
		if !ok {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrTOTPInvalid
		}
		// A code stays valid ~90s, so the step it consumed must be refused.
		if u.TOTPLastStep != 0 && step <= u.TOTPLastStep {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrTOTPInvalid
		}
		u.TOTPLastStep = step
		if err := users.PutUser(ctx, u); err != nil {
			return TOTPChallengeResult{}, err
		}
		factor = FactorTOTP
	case strings.TrimSpace(recoveryCode) != "":
		remaining, ok := consumeRecoveryCode(u.RecoveryCodeHashes, recoveryCode)
		if !ok {
			recordWrongGuess()
			return TOTPChallengeResult{}, ErrRecoveryCodeInvalid
		}
		u.RecoveryCodeHashes = remaining
		if err := users.PutUser(ctx, u); err != nil {
			return TOTPChallengeResult{}, err
		}
		factor = FactorRecoveryCode
	default:
		return TOTPChallengeResult{}, ErrTOTPInvalid
	}

	_ = challenges.Delete(ctx, token)
	if c.Tenant != "" {
		u.Tenant = c.Tenant
		u.Workspace = c.Workspace
		u.Roles = c.Roles
	}
	return TOTPChallengeResult{User: u, Factor: factor}, nil
}

// Single-use: a matched code is removed, so it cannot be replayed.
func consumeRecoveryCode(hashes []string, plaintext string) ([]string, bool) {
	plaintext = canonicaliseRecoveryCode(plaintext)
	matchIdx := -1
	for i, h := range hashes {
		if bcrypt.CompareHashAndPassword([]byte(h), []byte(plaintext)) == nil && matchIdx == -1 {
			matchIdx = i
		}
	}
	if matchIdx == -1 {
		return hashes, false
	}
	remaining := make([]string, 0, len(hashes)-1)
	remaining = append(remaining, hashes[:matchIdx]...)
	remaining = append(remaining, hashes[matchIdx+1:]...)
	return remaining, true
}

var recoveryCodeHashCost = func() int {
	if testing.Testing() {
		return testPasswordHashCost
	}
	return bcrypt.DefaultCost
}()

func generateRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, totpRecoveryCodeCount)
	hashes := make([]string, totpRecoveryCodeCount)
	for i := range codes {
		raw, err := randomRecoveryCodeChars(8)
		if err != nil {
			return nil, nil, err
		}
		codes[i] = raw[:4] + "-" + raw[4:]
		h, err := bcrypt.GenerateFromPassword(
			[]byte(canonicaliseRecoveryCode(codes[i])),
			recoveryCodeHashCost,
		)
		if err != nil {
			return nil, nil, err
		}
		hashes[i] = string(h)
	}
	return codes, hashes, nil
}

func randomRecoveryCodeChars(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	alpha := totpRecoveryCodeAlphabet
	for i, x := range b {
		out[i] = alpha[int(x)%len(alpha)]
	}
	return string(out), nil
}

// Normalised before hashing, so case and dashes at redemption do not matter.
func canonicaliseRecoveryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// 192 bits, so guessing a live challenge is infeasible.
func newChallengeToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
