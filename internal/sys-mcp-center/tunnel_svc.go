// Package center provides the gRPC TunnelService implementation for sys-mcp-center.
package center

import (
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jimyag/sys-mcp/api/tunnel"
	pkgstream "github.com/jimyag/sys-mcp/internal/pkg/stream"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
)

// TunnelServiceServer implements the gRPC TunnelService for center.
type TunnelServiceServer struct {
	tunnel.UnimplementedTunnelServiceServer
	reg         *registry.Registry
	router      *router.Router
	agentTokens []string
	logger      *slog.Logger
}

// NewTunnelServiceServer creates a TunnelServiceServer.
func NewTunnelServiceServer(reg *registry.Registry, rtr *router.Router, agentTokens []string, logger *slog.Logger) *TunnelServiceServer {
	return &TunnelServiceServer{
		reg:         reg,
		router:      rtr,
		agentTokens: agentTokens,
		logger:      logger,
	}
}

// Connect handles a bidirectional stream from an agent or proxy.
func (s *TunnelServiceServer) Connect(srv tunnel.TunnelService_ConnectServer) error {
	// First message must be REGISTER_REQ.
	first, err := srv.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "recv first message: %v", err)
	}
	req := first.GetRegisterRequest()
	if req == nil {
		return status.Error(codes.InvalidArgument, "first message must be RegisterRequest")
	}

	// Authenticate.
	if !s.validToken(req.Token) {
		_ = srv.Send(&tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_RegisterAck{
				RegisterAck: &tunnel.RegisterAck{Success: false, Message: "invalid token"},
			},
		})
		return status.Error(codes.Unauthenticated, "invalid token")
	}

	streamID := pkgstream.NewRequestID("stream")
	ts := pkgstream.WrapServerStream(streamID, srv)

	// Register the agent.
	nodeType := "agent"
	if req.NodeType == tunnel.NodeType_NODE_TYPE_PROXY {
		nodeType = "proxy"
	}
	rec := &registry.AgentRecord{
		Hostname:      req.Hostname,
		IP:            req.Ip,
		OS:            req.Os,
		AgentVersion:  req.AgentVersion,
		NodeType:      nodeType,
		ProxyPath:     req.ProxyPath,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		Status:        registry.StatusOnline,
		RouteStream:   ts,
	}
	s.reg.Register(rec)
	s.logger.Info("agent registered", "hostname", req.Hostname, "type", nodeType, "ip", req.Ip)

	// Send ack.
	if err := ts.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterAck{
			RegisterAck: &tunnel.RegisterAck{Success: true},
		},
	}); err != nil {
		s.reg.UnregisterByStream(ts)
		return err
	}

	defer func() {
		removed := s.reg.UnregisterByStream(ts)
		for _, h := range removed {
			s.logger.Info("agent unregistered", "hostname", h)
		}
	}()

	// Read loop.
	for {
		msg, err := ts.Recv()
		if err != nil {
			return nil
		}
		switch p := msg.Payload.(type) {
		case *tunnel.TunnelMessage_Heartbeat:
			s.reg.UpdateHeartbeat(req.Hostname)
			_ = ts.Send(&tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_HeartbeatAck{
					HeartbeatAck: &tunnel.HeartbeatAck{
						TimestampMs: p.Heartbeat.TimestampMs,
					},
				},
			})
		case *tunnel.TunnelMessage_ToolResponse:
			s.router.DeliverFromMessage(msg)
		case *tunnel.TunnelMessage_ErrorResponse:
			s.router.DeliverFromMessage(msg)
		case *tunnel.TunnelMessage_RegisterRequest:
			// Proxy forwarding a downstream agent's registration or heartbeat refresh.
			// If the agent is already known, only refresh LastHeartbeat to preserve RegisteredAt.
			hostname := p.RegisterRequest.Hostname
			if s.reg.Lookup(hostname) != nil {
				s.reg.UpdateHeartbeat(hostname)
				s.logger.Debug("proxy-forwarded heartbeat received",
					"hostname", hostname, "via_proxy", req.Hostname)
			} else {
				downstreamNodeType := "agent"
				if p.RegisterRequest.NodeType == tunnel.NodeType_NODE_TYPE_PROXY {
					downstreamNodeType = "proxy"
				}
				downstreamRec := &registry.AgentRecord{
					Hostname:      hostname,
					IP:            p.RegisterRequest.Ip,
					OS:            p.RegisterRequest.Os,
					AgentVersion:  p.RegisterRequest.AgentVersion,
					NodeType:      downstreamNodeType,
					ProxyPath:     p.RegisterRequest.ProxyPath,
					RegisteredAt:  time.Now(),
					LastHeartbeat: time.Now(),
					Status:        registry.StatusOnline,
					RouteStream:   ts, // route via the proxy's stream
				}
				s.reg.Register(downstreamRec)
				s.logger.Info("proxy-forwarded agent registered",
					"hostname", hostname,
					"via_proxy", req.Hostname,
				)
			}
		}
	}
}

func (s *TunnelServiceServer) validToken(token string) bool {
	token = strings.TrimPrefix(token, "Bearer ")
	for _, t := range s.agentTokens {
		if t == token {
			return true
		}
	}
	return false
}
