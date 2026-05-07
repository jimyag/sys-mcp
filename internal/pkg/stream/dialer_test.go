package stream_test

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/jimyag/sysplane/api/tunnel"
	pkgstream "github.com/jimyag/sysplane/internal/pkg/stream"
)

// mockTunnelServer accepts one stream, sends ack, then closes.
type mockTunnelServer struct {
	tunnel.UnimplementedTunnelServiceServer
	connectCount atomic.Int32
}

func (m *mockTunnelServer) Connect(stream tunnel.TunnelService_ConnectServer) error {
	m.connectCount.Add(1)
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	req := msg.GetRegisterRequest()
	if req == nil {
		return nil
	}
	// Send register ack.
	if err := stream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterAck{
			RegisterAck: &tunnel.RegisterAck{Success: true},
		},
	}); err != nil {
		return err
	}
	// Keep open briefly then close to trigger reconnect.
	time.Sleep(20 * time.Millisecond)
	return nil
}

func startMockServer(t *testing.T, srv *mockTunnelServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	tunnel.RegisterTunnelServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestDialerReconnects(t *testing.T) {
	srv := &mockTunnelServer{}
	addr := startMockServer(t, srv)

	d := pkgstream.NewDialer(pkgstream.DialerConfig{
		Endpoint: addr,
		RegisterMsg: &tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_RegisterRequest{
				RegisterRequest: &tunnel.RegisterRequest{
					Hostname: "test-host",
					NodeType: tunnel.NodeType_NODE_TYPE_AGENT,
					Token:    "tok",
				},
			},
		},
		HeartbeatInterval: 10 * time.Second,
		ReconnectMaxDelay: 100 * time.Millisecond,
		TLSCredentials:    insecure.NewCredentials(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() { _ = d.Run(ctx) }()

	// Wait for at least 2 connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.connectCount.Load() >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected at least 2 reconnects, got %d", srv.connectCount.Load())
}

func TestNewRequestID(t *testing.T) {
	id1 := pkgstream.NewRequestID("tool")
	id2 := pkgstream.NewRequestID("tool")
	if id1 == id2 {
		t.Fatal("expected unique request IDs")
	}
	if len(id1) == 0 {
		t.Fatal("expected non-empty request ID")
	}
}
