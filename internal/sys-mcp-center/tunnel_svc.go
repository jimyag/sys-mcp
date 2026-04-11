// Package center provides the gRPC TunnelService implementation for sys-mcp-center.
package center

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jimyag/sys-mcp/api/tunnel"
	pkgstream "github.com/jimyag/sys-mcp/internal/pkg/stream"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/store"
)

// AgentPersister 是 PG 写入的可选接口。
// store.Store 实现此接口；若数据库未启用则传 nil。
type AgentPersister interface {
	UpsertAgent(ctx context.Context, r *store.AgentRow) error
	UpdateAgentHeartbeat(ctx context.Context, hostname string) error
	SetAgentOffline(ctx context.Context, hostname string) error
}

// TunnelServiceServer implements the gRPC TunnelService for center.
type TunnelServiceServer struct {
	tunnel.UnimplementedTunnelServiceServer
	reg         *registry.Registry
	router      *router.Router
	agentTokens []string
	logger      *slog.Logger
	persister   AgentPersister // optional; nil if database is disabled
	instanceID  string         // center instance ID for PG writes
}

// NewTunnelServiceServer creates a TunnelServiceServer.
// persister and instanceID are optional; pass nil/"" to disable PG writes.
func NewTunnelServiceServer(reg *registry.Registry, rtr *router.Router, agentTokens []string, logger *slog.Logger, persister AgentPersister, instanceID string) *TunnelServiceServer {
	return &TunnelServiceServer{
		reg:         reg,
		router:      rtr,
		agentTokens: agentTokens,
		logger:      logger,
		persister:   persister,
		instanceID:  instanceID,
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

	if s.persister != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.persister.UpsertAgent(ctx, &store.AgentRow{
			Hostname:      req.Hostname,
			IP:            req.Ip,
			OS:            req.Os,
			AgentVersion:  req.AgentVersion,
			NodeType:      nodeType,
			ProxyPath:     req.ProxyPath,
			CenterID:      s.instanceID,
			Status:        "online",
			RegisteredAt:  rec.RegisteredAt,
			LastHeartbeat: rec.LastHeartbeat,
		})
		cancel()
		if err != nil {
			_ = srv.Send(&tunnel.TunnelMessage{
				Payload: &tunnel.TunnelMessage_RegisterAck{
					RegisterAck: &tunnel.RegisterAck{Success: false, Message: "persist registration failed"},
				},
			})
			return status.Errorf(codes.Unavailable, "persist registration: %v", err)
		}
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
			if s.persister != nil {
				// Mark offline immediately on stream close so the PG record reflects
				// the real state without waiting for the next offline-checker cycle.
				// The offline checker may also call SetAgentOffline later (~15s), but
				// that is an idempotent UPDATE and acceptable as a no-op.
				go func(hostname string) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := s.persister.SetAgentOffline(ctx, hostname); err != nil {
						s.logger.Warn("SetAgentOffline failed", "hostname", hostname, "error", err)
					}
				}(h)
			}
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
			if s.persister != nil {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := s.persister.UpdateAgentHeartbeat(ctx, req.Hostname); err != nil {
						s.logger.Debug("UpdateAgentHeartbeat failed", "hostname", req.Hostname, "error", err)
					}
				}()
			}
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
				if s.persister != nil {
					go func(h string) {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						if err := s.persister.UpdateAgentHeartbeat(ctx, h); err != nil {
							s.logger.Debug("UpdateAgentHeartbeat (proxy) failed", "hostname", h, "error", err)
						}
					}(hostname)
				}
			} else {
				downstreamNodeType := "agent"
				if p.RegisterRequest.NodeType == tunnel.NodeType_NODE_TYPE_PROXY {
					downstreamNodeType = "proxy"
				}
				now := time.Now()
				downstreamRec := &registry.AgentRecord{
					Hostname:      hostname,
					IP:            p.RegisterRequest.Ip,
					OS:            p.RegisterRequest.Os,
					AgentVersion:  p.RegisterRequest.AgentVersion,
					NodeType:      downstreamNodeType,
					ProxyPath:     p.RegisterRequest.ProxyPath,
					RegisteredAt:  now,
					LastHeartbeat: now,
					Status:        registry.StatusOnline,
					RouteStream:   ts, // route via the proxy's stream
				}
				s.reg.Register(downstreamRec)
				s.logger.Info("proxy-forwarded agent registered",
					"hostname", hostname,
					"via_proxy", req.Hostname,
				)
				if s.persister != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := s.persister.UpsertAgent(ctx, &store.AgentRow{
						Hostname:      downstreamRec.Hostname,
						IP:            downstreamRec.IP,
						OS:            downstreamRec.OS,
						AgentVersion:  downstreamRec.AgentVersion,
						NodeType:      downstreamRec.NodeType,
						ProxyPath:     downstreamRec.ProxyPath,
						CenterID:      s.instanceID,
						Status:        "online",
						RegisteredAt:  downstreamRec.RegisteredAt,
						LastHeartbeat: downstreamRec.LastHeartbeat,
					})
					cancel()
					if err != nil {
						s.reg.Unregister(hostname)
						s.logger.Warn("UpsertAgent (proxy) failed", "hostname", downstreamRec.Hostname, "error", err)
					}
				}
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
