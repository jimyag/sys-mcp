package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunNodesList(t *testing.T) {
	var gotAuth string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []map[string]any{{"id": "node-1"}},
		})
	}))
	t.Cleanup(srv.Close)

	var stdout bytes.Buffer
	app := &App{
		stdout: stdoutWriter{&stdout},
		stderr: io.Discard,
		client: srv.Client(),
		getenv: func(string) string { return "" },
		now:    fixedNow,
	}
	if err := app.Run([]string{"--server", srv.URL, "--token", "tok", "nodes", "list", "--limit", "10", "--status", "online"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotQuery, "limit=10") || !strings.Contains(gotQuery, "status=online") {
		t.Fatalf("query = %q", gotQuery)
	}
	if !strings.Contains(stdout.String(), "\"node-1\"") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunCommandsInvokeBuiltinFSReadReturnsInvocationJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"invocation": map[string]any{"id": "inv-read"},
			"results": []map[string]any{
				{"data": map[string]any{"content": "hello"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	var stdout bytes.Buffer
	app := &App{
		stdout: stdoutWriter{&stdout},
		stderr: io.Discard,
		client: srv.Client(),
		getenv: func(string) string { return "" },
		now:    fixedNow,
	}
	if err := app.Run([]string{"--server", srv.URL, "--token", "tok", "commands", "invoke", "fs.read", "--node", "node-1", "--params", "{\"path\":\"/etc/hosts\"}"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stdout.String(), "\"inv-read\"") || !strings.Contains(stdout.String(), "\"hello\"") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunTemplateInvokeByName(t *testing.T) {
	var requested []string
	var invoked map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = append(requested, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/command-templates"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"id": "tpl-123", "name": "echo.hello"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/invocations":
			_ = json.NewDecoder(r.Body).Decode(&invoked)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"invocation": map[string]any{"id": "inv-1"},
				"results":    []map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	var stdout bytes.Buffer
	app := &App{
		stdout: stdoutWriter{&stdout},
		stderr: io.Discard,
		client: srv.Client(),
		getenv: func(string) string { return "" },
		now:    fixedNow,
	}
	if err := app.Run([]string{"--server", srv.URL, "--token", "tok", "commands", "invoke", "echo.hello", "--nodes", "node-1"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(requested) != 2 {
		t.Fatalf("requests = %#v", requested)
	}
	if requested[1] != "POST /v1/invocations" {
		t.Fatalf("second request = %q", requested[1])
	}
	if invoked["action"] != "tpl-123" || invoked["action_type"] != "command_template" {
		t.Fatalf("invocation payload = %#v", invoked)
	}
	if !strings.Contains(stdout.String(), "\"inv-1\"") {
		t.Fatalf("stdout = %s", stdout.String())
	}
}

func TestRunRequiresToken(t *testing.T) {
	app := &App{
		stdout: io.Discard,
		stderr: io.Discard,
		client: &http.Client{},
		getenv: func(string) string { return "" },
		now:    fixedNow,
	}
	if err := app.Run([]string{"nodes", "list"}); err == nil || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("Run() error = %v, want missing token", err)
	}
}

type stdoutWriter struct{ io.Writer }

func fixedNow() time.Time {
	return time.Date(2026, 5, 7, 21, 30, 0, 0, time.UTC)
}
