package stream

import (
	"context"
	"net"
	"sync"

	"google.golang.org/grpc/peer"

	"github.com/jimyag/sys-mcp/api/tunnel"
)

// TunnelStream is the server-side abstraction over a gRPC bidirectional stream.
type TunnelStream interface {
	// Send sends a message to the remote end (thread-safe).
	Send(msg *tunnel.TunnelMessage) error
	// Recv blocks until a message arrives or the stream closes.
	Recv() (*tunnel.TunnelMessage, error)
	// ID returns the unique identifier assigned at wrap time.
	ID() string
	// RemoteAddr returns the peer address string, or "unknown".
	RemoteAddr() string
	// Context returns the stream's context (cancelled when stream closes).
	Context() context.Context
}

type tunnelStream struct {
	id         string
	srv        tunnel.TunnelService_ConnectServer
	cancelFunc context.CancelFunc
	mu         sync.Mutex
}

// WrapServerStream wraps a server-side gRPC stream into a TunnelStream.
// cancel is called when Close is invoked (e.g. when a handler wants to terminate the stream).
func WrapServerStream(id string, srv tunnel.TunnelService_ConnectServer) TunnelStream {
	return &tunnelStream{id: id, srv: srv}
}

func (s *tunnelStream) ID() string { return s.id }

func (s *tunnelStream) Send(msg *tunnel.TunnelMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv.Send(msg)
}

func (s *tunnelStream) Recv() (*tunnel.TunnelMessage, error) {
	return s.srv.Recv()
}

func (s *tunnelStream) Context() context.Context {
	return s.srv.Context()
}

func (s *tunnelStream) RemoteAddr() string {
	p, ok := peer.FromContext(s.srv.Context())
	if !ok || p.Addr == nil {
		return "unknown"
	}
	if addr, ok := p.Addr.(net.Addr); ok {
		return addr.String()
	}
	return p.Addr.String()
}
