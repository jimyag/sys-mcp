package center_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/jimyag/sys-mcp/api/tunnel"
	center "github.com/jimyag/sys-mcp/internal/sys-mcp-center"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/store"
)

const bufSize = 1 << 20 // 1 MiB

// newTestServer spins up an in-process gRPC server with TunnelServiceServer
// and returns a connected client plus the registry for assertions.
func newTestServer(t *testing.T, tokens []string) (tunnel.TunnelServiceClient, *registry.Registry, func()) {
	t.Helper()

	reg := registry.New()
	rtr := router.New(5)
	logger := slog.Default()
	svc := center.NewTunnelServiceServer(reg, rtr, tokens, logger, nil, "")

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	tunnel.RegisterTunnelServiceServer(srv, svc)

	go func() {
		_ = srv.Serve(lis)
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return lis.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	client := tunnel.NewTunnelServiceClient(conn)
	cleanup := func() {
		conn.Close()
		srv.Stop()
		lis.Close()
	}
	return client, reg, cleanup
}

// connect opens a new bidirectional stream from the given client.
func connect(ctx context.Context, t *testing.T, client tunnel.TunnelServiceClient) tunnel.TunnelService_ConnectClient {
	t.Helper()
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	return stream
}

// register sends a RegisterRequest and returns the RegisterAck received.
func register(t *testing.T, stream tunnel.TunnelService_ConnectClient, hostname, token string) *tunnel.RegisterAck {
	t.Helper()
	err := stream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterRequest{
			RegisterRequest: &tunnel.RegisterRequest{
				Hostname: hostname,
				Token:    token,
				NodeType: tunnel.NodeType_NODE_TYPE_AGENT,
			},
		},
	})
	if err != nil {
		t.Fatalf("Send RegisterRequest: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv RegisterAck: %v", err)
	}
	ack := msg.GetRegisterAck()
	if ack == nil {
		t.Fatalf("expected RegisterAck, got %T", msg.Payload)
	}
	return ack
}

func TestTunnelSvc_AgentReregistration(t *testing.T) {
	client, reg, cleanup := newTestServer(t, []string{"tok"})
	defer cleanup()

	// ── First connection ──────────────────────────────────────────────────────
	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	stream1 := connect(ctx1, t, client)
	ack1 := register(t, stream1, "test-agent", "tok")
	if !ack1.Success {
		t.Fatalf("first registration failed: %s", ack1.Message)
	}

	// Agent should be in the registry.
	rec1 := reg.Lookup("test-agent")
	if rec1 == nil {
		t.Fatal("expected agent in registry after first registration, got nil")
	}
	firstStream := rec1.RouteStream

	// Close the stream to simulate agent disconnect.
	_ = stream1.CloseSend()
	// Drain the stream so the server-side handler gets the EOF.
	for {
		_, err := stream1.Recv()
		if err != nil {
			break
		}
	}

	// Give the server goroutine time to run the deferred UnregisterByStream.
	time.Sleep(150 * time.Millisecond)

	// Agent should have been removed from the registry.
	if got := reg.Lookup("test-agent"); got != nil {
		t.Fatalf("expected agent to be removed after disconnect, still present with status=%s", got.Status)
	}

	// ── Second connection ─────────────────────────────────────────────────────
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	stream2 := connect(ctx2, t, client)
	ack2 := register(t, stream2, "test-agent", "tok")
	if !ack2.Success {
		t.Fatalf("second registration failed: %s", ack2.Message)
	}

	rec2 := reg.Lookup("test-agent")
	if rec2 == nil {
		t.Fatal("expected agent in registry after re-registration, got nil")
	}
	if rec2.Status != registry.StatusOnline {
		t.Fatalf("expected status Online after re-registration, got %s", rec2.Status)
	}
	if rec2.RouteStream == firstStream {
		t.Fatal("expected a new RouteStream after re-registration, but got the same one")
	}

	// Clean up the second stream.
	_ = stream2.CloseSend()
	for {
		_, err := stream2.Recv()
		if err != nil {
			break
		}
	}
}

func TestTunnelSvc_InvalidToken(t *testing.T) {
	client, reg, cleanup := newTestServer(t, []string{"tok"})
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := connect(ctx, t, client)

	err := stream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterRequest{
			RegisterRequest: &tunnel.RegisterRequest{
				Hostname: "bad-agent",
				Token:    "bad-token",
				NodeType: tunnel.NodeType_NODE_TYPE_AGENT,
			},
		},
	})
	if err != nil {
		t.Fatalf("Send RegisterRequest: %v", err)
	}

	// The server should send an ack with Success=false, then close the stream.
	msg, err := stream.Recv()
	if err != nil && err != io.EOF {
		// Server may close immediately after sending the ack; that's fine.
		// But if we got a message, check it.
		t.Logf("Recv returned error (may be fine): %v", err)
	} else if msg != nil {
		ack := msg.GetRegisterAck()
		if ack == nil {
			t.Fatalf("expected RegisterAck, got %T", msg.Payload)
		}
		if ack.Success {
			t.Fatal("expected Success=false for invalid token, got true")
		}
	}

	// Registry must be empty.
	all := reg.All()
	if len(all) != 0 {
		t.Fatalf("expected empty registry after invalid token, got %d records", len(all))
	}
}

type blockingPersister struct {
	upsertStarted atomic.Bool
	release       chan struct{}
}

func (p *blockingPersister) UpsertAgent(ctx context.Context, r *store.AgentRow) error {
	p.upsertStarted.Store(true)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *blockingPersister) UpdateAgentHeartbeat(ctx context.Context, hostname string) error {
	return nil
}
func (p *blockingPersister) SetAgentOffline(ctx context.Context, hostname string) error { return nil }

func TestTunnelSvc_RegisterWaitsForInitialPersist(t *testing.T) {
	reg := registry.New()
	rtr := router.New(5)
	logger := slog.Default()
	p := &blockingPersister{release: make(chan struct{})}
	svc := center.NewTunnelServiceServer(reg, rtr, []string{"tok"}, logger, p, "center-01")

	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	tunnel.RegisterTunnelServiceServer(srv, svc)
	go func() { _ = srv.Serve(lis) }()
	defer func() {
		srv.Stop()
		lis.Close()
	}()

	dialer := func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer conn.Close()

	client := tunnel.NewTunnelServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if err := stream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterRequest{
			RegisterRequest: &tunnel.RegisterRequest{
				Hostname: "test-agent",
				Token:    "tok",
				NodeType: tunnel.NodeType_NODE_TYPE_AGENT,
			},
		},
	}); err != nil {
		t.Fatalf("Send RegisterRequest: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if !p.upsertStarted.Load() {
		t.Fatal("expected UpsertAgent to start before ack is sent")
	}

	ackCh := make(chan *tunnel.RegisterAck, 1)
	errCh := make(chan error, 1)
	go func() {
		msg, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		ackCh <- msg.GetRegisterAck()
	}()

	select {
	case <-ackCh:
		t.Fatal("did not expect register ack before initial persist completed")
	case err := <-errCh:
		t.Fatalf("unexpected recv error before persist release: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(p.release)

	select {
	case ack := <-ackCh:
		if ack == nil || !ack.Success {
			t.Fatalf("expected successful ack after persist release, got %+v", ack)
		}
	case err := <-errCh:
		t.Fatalf("unexpected recv error after persist release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ack after persist release")
	}
}
