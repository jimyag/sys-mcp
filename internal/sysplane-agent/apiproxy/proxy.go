// Package apiproxy provides a loopback-only HTTP proxy tool for the agent.
package apiproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Config holds proxy settings.
type Config struct {
	AllowPrivilegedPorts bool
	AllowedPorts         []int
}

// Proxy performs HTTP calls to loopback addresses only.
type Proxy struct {
	cfg    Config
	client *http.Client
}

// New creates a Proxy with the given config.
// The HTTP client's DialContext is overridden to block non-loopback connections,
// preventing DNS rebinding attacks.
func New(cfg Config) *Proxy {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("apiproxy: parse addr %q: %w", addr, err)
			}
			// Resolve the host to IPs.
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("apiproxy: resolve %q: %w", host, err)
			}
			for _, ip := range ips {
				parsed := net.ParseIP(ip)
				if parsed == nil || !parsed.IsLoopback() {
					return nil, fmt.Errorf("apiproxy: resolved %q to non-loopback IP %s", host, ip)
				}
			}
			// Always connect to loopback.
			return dialer.DialContext(ctx, network, "127.0.0.1:"+port)
		},
	}
	return &Proxy{
		cfg: cfg,
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// CallParams is the input for proxy_local_api.
type CallParams struct {
	Port    int               `json:"port"`
	Path    string            `json:"path"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// CallResult is the output of proxy_local_api.
type CallResult struct {
	RequestURL string            `json:"request_url"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodyIsJSON bool              `json:"body_is_json"`
	DurationMs int64             `json:"duration_ms"`
}

// Call executes a local HTTP request.
func (p *Proxy) Call(ctx context.Context, argsJSON string) (string, error) {
	var params CallParams
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return "", fmt.Errorf("proxy_local_api: invalid args: %w", err)
	}
	if err := p.checkPort(params.Port); err != nil {
		return "", err
	}

	if params.Method == "" {
		params.Method = http.MethodGet
	}
	if !strings.HasPrefix(params.Path, "/") {
		params.Path = "/" + params.Path
	}

	url := fmt.Sprintf("http://127.0.0.1:%d%s", params.Port, params.Path)
	var bodyReader io.Reader
	if params.Body != "" {
		bodyReader = strings.NewReader(params.Body)
	}

	req, err := http.NewRequestWithContext(ctx, params.Method, url, bodyReader)
	if err != nil {
		return "", fmt.Errorf("proxy_local_api: build request: %w", err)
	}
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("proxy_local_api: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start).Milliseconds()

	bodyBytes, _ := io.ReadAll(resp.Body)
	bodyStr := string(bodyBytes)

	respHeaders := make(map[string]string, len(resp.Header))
	for k := range resp.Header {
		respHeaders[k] = resp.Header.Get(k)
	}

	bodyIsJSON := json.Valid(bodyBytes) && len(bodyBytes) > 0

	res := CallResult{
		RequestURL: url,
		StatusCode: resp.StatusCode,
		Headers:    respHeaders,
		Body:       bodyStr,
		BodyIsJSON: bodyIsJSON,
		DurationMs: elapsed,
	}
	out, _ := json.Marshal(res)
	return string(out), nil
}

func (p *Proxy) checkPort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("proxy_local_api: invalid port %d", port)
	}
	if !p.cfg.AllowPrivilegedPorts && port < 1024 {
		return fmt.Errorf("proxy_local_api: privileged port %d not allowed", port)
	}
	if len(p.cfg.AllowedPorts) > 0 {
		for _, allowed := range p.cfg.AllowedPorts {
			if port == allowed {
				return nil
			}
		}
		return fmt.Errorf("proxy_local_api: port %d not in allowed list", port)
	}
	return nil
}
