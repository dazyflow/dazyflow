// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// AwsSecretsProvider is the AWS flavour of the BYO secret manager (see
// VaultProvider for the model): it resolves ${aws.NAME} / ${aws.NAME#field}
// against each TENANT's own AWS Secrets Manager. NAME is the secret name (or
// full ARN); the optional #field plucks one key out of a JSON SecretString —
// the common "one secret, many fields" layout the AWS console encourages.
//
// The API client is hand-rolled (SigV4 + one JSON-RPC call) rather than
// pulling in aws-sdk-go-v2: the provider needs exactly GetSecretValue, and
// the SDK is a dependency tree two orders of magnitude bigger than the ~100
// lines of signing below. Same trade /metrics and the Stripe client made.
type AwsSecretsProvider struct {
	client *awsAPIClient
	// loadConfig returns the calling tenant's connection config. ok=false
	// means the tenant hasn't configured AWS (a clear "not configured"
	// error, not a failure). Backed by the encrypted store in production.
	loadConfig func(ctx context.Context, tenant string) (cfg AwsSecretsConfig, ok bool, err error)
	cache      *tenantSecretCache
}

// NewAwsSecretsProvider builds the provider. ttl <= 0 uses defaultVaultCacheTTL
// (the BYO providers share one staleness policy).
func NewAwsSecretsProvider(client *awsAPIClient, loadConfig func(context.Context, string) (AwsSecretsConfig, bool, error), ttl time.Duration) *AwsSecretsProvider {
	return &AwsSecretsProvider{client: client, loadConfig: loadConfig, cache: newTenantSecretCache(ttl)}
}

// NewAwsSecretsProviderForStore builds the production provider with each
// tenant's config loaded from the encrypted store.
func NewAwsSecretsProviderForStore(es *EncryptedSecrets, httpTimeout time.Duration) *AwsSecretsProvider {
	return NewAwsSecretsProvider(
		newAwsAPIClient(httpTimeout),
		func(ctx context.Context, tenant string) (AwsSecretsConfig, bool, error) {
			return loadProviderConfig[AwsSecretsConfig](ctx, es, tenant, awsConfigSecretName)
		},
		0,
	)
}

// VerifyAwsConfig validates a config and checks the credentials authenticate,
// using a one-off client. Used by the save endpoint to catch a bad region/key
// before persisting it.
func VerifyAwsConfig(ctx context.Context, cfg AwsSecretsConfig, timeout time.Duration) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	return newAwsAPIClient(timeout).verify(ctx, cfg)
}

func (p *AwsSecretsProvider) Scheme() string { return "aws" }

// Get resolves "NAME" or "NAME#field": fetches the secret's current value
// from the tenant's Secrets Manager; with #field, the SecretString must be a
// JSON object and the named key is returned. The tenant comes from context —
// a BYO secret is always tenant-scoped, never global.
func (p *AwsSecretsProvider) Get(ctx context.Context, ref string) (string, error) {
	return getCloudSecret(ctx, "aws", ref, p.cache, p.loadConfig, p.client.getSecretValue)
}

// AwsSecretsConfig is one tenant's connection to AWS Secrets Manager. Static
// access-key credentials for V1 (an IAM user or long-lived key scoped to
// secretsmanager:GetSecretValue); IAM-role / STS assume-role is a follow-up.
// It carries credentials, so it is itself a secret — stored encrypted.
type AwsSecretsConfig struct {
	Region          string `json:"region"`            // e.g. eu-north-1
	AccessKeyID     string `json:"access_key_id"`     // AKIA…
	SecretAccessKey string `json:"secret_access_key"` //
	// Endpoint overrides the API host — LocalStack / tests. Empty uses
	// https://secretsmanager.{region}.amazonaws.com.
	Endpoint string `json:"endpoint,omitempty"`
}

func (c AwsSecretsConfig) validate() error {
	if c.Region == "" {
		return fmt.Errorf("region is required (e.g. eu-north-1)")
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return fmt.Errorf("access_key_id and secret_access_key are required")
	}
	if c.Endpoint != "" && !strings.HasPrefix(c.Endpoint, "https://") && !strings.HasPrefix(c.Endpoint, "http://") {
		return fmt.Errorf("endpoint must be an http(s) URL")
	}
	return nil
}

func (c AwsSecretsConfig) endpointURL() string {
	if c.Endpoint != "" {
		return strings.TrimRight(c.Endpoint, "/")
	}
	return "https://secretsmanager." + c.Region + ".amazonaws.com"
}

// awsConfigSecretName is the reserved encrypted-store key for a tenant's AWS
// connection (the "cfg:" prefix hides it from user-facing listings).
const awsConfigSecretName = "cfg:secret-manager-aws"

// awsAPIClient speaks Secrets Manager's JSON-RPC ("x-amz-json-1.1") protocol
// with hand-rolled SigV4 request signing. The endpoint is tenant-supplied, so
// like every other outbound path that dials a tenant-controlled host it routes
// through the shared SSRF guard (post-DNS, rebinding-resistant; a no-op when the
// operator opted into private egress). Defense in depth even though configuring
// the endpoint now requires organization:admin — it stops an org admin from
// turning the verify/fetch call into a probe of the host's internal network or
// cloud metadata endpoint.
type awsAPIClient struct {
	httpc *http.Client
}

func newAwsAPIClient(timeout time.Duration) *awsAPIClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &awsAPIClient{httpc: hfnet.SafeHTTPClient(timeout, hfnet.PrivateEgressAllowed())}
}

// awsAPIError is Secrets Manager's error envelope.
type awsAPIError struct {
	Type    string `json:"__type"`
	Message string `json:"message"`
}

func (c *awsAPIClient) call(ctx context.Context, cfg AwsSecretsConfig, target string, reqBody any) ([]byte, *awsAPIError, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	endpoint := cfg.endpointURL()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", target)
	if err := signSigV4(req, payload, cfg, nowFunc().UTC()); err != nil {
		return nil, nil, err
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var ae awsAPIError
		if json.Unmarshal(raw, &ae) == nil && ae.Type != "" {
			// "com.amazonaws...#ResourceNotFoundException" → short name.
			if i := strings.LastIndexByte(ae.Type, '#'); i >= 0 {
				ae.Type = ae.Type[i+1:]
			}
			return nil, &ae, nil
		}
		return nil, nil, fmt.Errorf("aws returned %d: %s", resp.StatusCode, truncateForError(raw))
	}
	return raw, nil, nil
}

func (c *awsAPIClient) getSecretValue(ctx context.Context, cfg AwsSecretsConfig, name string) (string, error) {
	raw, apiErr, err := c.call(ctx, cfg, "secretsmanager.GetSecretValue", map[string]string{"SecretId": name})
	if err != nil {
		return "", err
	}
	if apiErr != nil {
		return "", fmt.Errorf("%s: %s", apiErr.Type, apiErr.Message)
	}
	var out struct {
		SecretString string `json:"SecretString"`
		SecretBinary string `json:"SecretBinary"` // base64; passed through verbatim
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if out.SecretString != "" {
		return out.SecretString, nil
	}
	if out.SecretBinary != "" {
		return out.SecretBinary, nil
	}
	return "", fmt.Errorf("secret has neither SecretString nor SecretBinary")
}

// verify proves the credentials are accepted by calling GetSecretValue on a
// probe name that should not exist. ResourceNotFound (or AccessDenied — the
// key works but is scoped tighter than the probe) means the signature was
// accepted; signature/identity errors mean the credentials are wrong.
func (c *awsAPIClient) verify(ctx context.Context, cfg AwsSecretsConfig) error {
	_, apiErr, err := c.call(ctx, cfg, "secretsmanager.GetSecretValue",
		map[string]string{"SecretId": "dazyflow-connection-test"})
	if err != nil {
		return err
	}
	if apiErr == nil {
		return nil // the probe secret actually exists — fine
	}
	switch apiErr.Type {
	case "ResourceNotFoundException", "AccessDeniedException":
		return nil
	}
	return fmt.Errorf("%s: %s", apiErr.Type, apiErr.Message)
}

func truncateForError(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

// signSigV4 signs req in place per AWS Signature Version 4
// (https://docs.aws.amazon.com/IAM/latest/UserGuide/create-signed-request.html)
// for the secretsmanager service. Only what this client emits is canonicalized
// (POST /, no query string, fixed header set) — not a general-purpose signer.
func signSigV4(req *http.Request, payload []byte, cfg AwsSecretsConfig, now time.Time) error {
	const service = "secretsmanager"
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	u, err := url.Parse(cfg.endpointURL())
	if err != nil {
		return err
	}
	host := u.Host
	req.Header.Set("X-Amz-Date", amzDate)
	req.Host = host

	payloadHash := hex.EncodeToString(sha256Sum(payload))
	canonicalHeaders := "content-type:" + req.Header.Get("Content-Type") + "\n" +
		"host:" + host + "\n" +
		"x-amz-date:" + amzDate + "\n" +
		"x-amz-target:" + req.Header.Get("X-Amz-Target") + "\n"
	const signedHeaders = "content-type;host;x-amz-date;x-amz-target"
	canonicalRequest := strings.Join([]string{
		http.MethodPost, "/", "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, cfg.Region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope,
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+cfg.SecretAccessKey), dateStamp)
	kRegion := hmacSHA256(kDate, cfg.Region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKeyID, scope, signedHeaders, signature))
	return nil
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

var _ core.SecretProvider = (*AwsSecretsProvider)(nil)
