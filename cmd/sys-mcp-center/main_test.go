package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jimyag/sys-mcp/api/tunnel"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
)

type fakeCallLogger struct {
	inserted  []string
	completed []string
}

func (l *fakeCallLogger) InsertToolCallLog(_ context.Context, requestID, _, _, _, _ string) error {
	l.inserted = append(l.inserted, requestID)
	return nil
}

func (l *fakeCallLogger) CompleteToolCallLog(_ context.Context, requestID, _, _ string) error {
	l.completed = append(l.completed, requestID)
	return nil
}

type fakeRouteStream struct{}

func (f *fakeRouteStream) Send(msg *tunnel.TunnelMessage) error { return nil }
func (f *fakeRouteStream) Recv() (*tunnel.TunnelMessage, error) { return nil, nil }
func (f *fakeRouteStream) Context() context.Context             { return context.Background() }
func (f *fakeRouteStream) ID() string                           { return "fake" }
func (f *fakeRouteStream) RemoteAddr() string                   { return "127.0.0.1:0" }

func TestInternalForwardHandler_LogsExecutedCall(t *testing.T) {
	reg := registry.New()
	reg.Register(&registry.AgentRecord{
		Hostname:    "agent-01",
		NodeType:    "agent",
		Status:      registry.StatusOnline,
		RouteStream: &fakeRouteStream{},
	})
	rtr := router.New(1)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	callLog := &fakeCallLogger{}

	handler := makeInternalForwardHandler(reg, rtr, "secret", logger, callLog, "center-b")

	body := []byte(`{"request_id":"forward-001","tool_name":"get_hardware_info","args_json":"{}","target_host":"agent-01"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/forward", bytes.NewReader(body))
	req.Header.Set("X-Internal-Auth", "secret")
	w := httptest.NewRecorder()

	handler(w, req)

	if len(callLog.inserted) != 1 || callLog.inserted[0] != "forward-001" {
		t.Fatalf("expected executed call to be inserted once, got %+v", callLog.inserted)
	}
	if len(callLog.completed) != 1 || callLog.completed[0] != "forward-001" {
		t.Fatalf("expected executed call to be completed once, got %+v", callLog.completed)
	}
}
