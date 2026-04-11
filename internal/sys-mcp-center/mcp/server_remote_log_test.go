package mcp_test

import (
	"context"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/mcp"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
)

func TestRemoteForwardedTool_LogsAtEntryCenter(t *testing.T) {
	reg := registry.New()
	rtr := router.New(1)
	fwd := &fakeForwarder{returnJSON: `{"ok":true}`}
	log := &fakeLogger{}

	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	srv := mcp.BuildServerForTest(reg, rtr, fwd, log, "entry-center")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Run(ctx, serverTransport)
	}()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer cs.Close()

	args := map[string]any{"target_host": "remote-agent"}
	result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "get_hardware_info", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result.IsError {
		t.Fatalf("expected success, got error result: %+v", result)
	}
	if !fwd.called {
		t.Fatal("expected remote forwarder to be called")
	}
	if log.inserts != 1 {
		t.Fatalf("expected remote forwarded call to insert one entry-side log, got %d", log.inserts)
	}
	if log.completes != 1 {
		t.Fatalf("expected remote forwarded call to complete one entry-side log, got %d", log.completes)
	}
}
