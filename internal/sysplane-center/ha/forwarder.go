package ha

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// internalHTTPClient is a dedicated HTTP client for inter-instance calls.
// Avoids sharing http.DefaultClient and sets sane limits.
// No client-level Timeout is set here — every call uses context.WithTimeout(30s),
// so a separate client Timeout would never fire and would only cause confusion.
var internalHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
	},
}

// internalHTTPClientTLSSkipVerify is used for internal forwarding when TLS is enabled
// but certificate verification should be skipped (e.g., self-signed certs).
var internalHTTPClientTLSSkipVerify = &http.Client{
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     60 * time.Second,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // intentional for internal traffic
	},
}

// internalAuthHeader is the HTTP header used to authenticate /internal/forward calls.
const internalAuthHeader = "X-Internal-Auth"

// ForwardRequest 是跨 center 实例转发工具请求的 HTTP 请求体。
type ForwardRequest struct {
	RequestID  string `json:"request_id"`
	ToolName   string `json:"tool_name"`
	ArgsJSON   string `json:"args_json"`
	TargetHost string `json:"target_host"`
}

// ForwardResponse 是转发结果。
type ForwardResponse struct {
	ResultJSON string `json:"result_json,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ForwardToCenter 将工具请求 HTTP POST 到指定 center 实例的 /internal/forward 端点。
// secret 是共享密钥，与接收方 config.HA.InternalSecret 一致。
// useTLS 为 true 时使用 https://，否则使用 http://。
// skipVerify 为 true 时跳过 TLS 证书验证（适用于自签名证书）。
func ForwardToCenter(ctx context.Context, internalAddress, secret string, req ForwardRequest, useTLS, skipVerify bool) (string, error) {
	body, _ := json.Marshal(req)

	scheme := "http"
	if useTLS {
		scheme = "https"
	}

	httpCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(
		httpCtx, http.MethodPost,
		scheme+"://"+internalAddress+"/internal/forward",
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("ha: build forward request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if secret != "" {
		httpReq.Header.Set(internalAuthHeader, secret)
	}

	client := internalHTTPClient
	if useTLS && skipVerify {
		client = internalHTTPClientTLSSkipVerify
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ha: forward request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("ha: /internal/forward rejected (401 Unauthorized) — check ha.internal_secret config")
	}

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	var fwdResp ForwardResponse
	if err := json.Unmarshal(data, &fwdResp); err != nil {
		return "", fmt.Errorf("ha: decode response: %w", err)
	}
	if fwdResp.Error != "" {
		return "", fmt.Errorf("ha: remote error: %s", fwdResp.Error)
	}
	return fwdResp.ResultJSON, nil
}
