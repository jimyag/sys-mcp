// Package tunnel implements the proxy's downstream (server-side) gRPC handler
// and upstream (client-side) dialer.
package tunnel

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jimyag/sys-mcp/api/tunnel"
	pkgstream "github.com/jimyag/sys-mcp/internal/pkg/stream"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/registry"
)

// Upstream is the interface the downstream service uses to forward messages
// to the upstream (center or parent proxy).
type Upstream interface {
	Send(msg *tunnel.TunnelMessage) error
}

// DownstreamService implements the gRPC TunnelService that agents connect to.
// It mirrors center's TunnelServiceServer but routes tool requests through the
// local registry and forwards them upstream.
type DownstreamService struct {
	tunnel.UnimplementedTunnelServiceServer
	reg           *registry.Registry
	agentTokens   []string
	upstream      Upstream
	proxyHostname string
	logger        *slog.Logger

	// pendingRequests maps requestID -> targetHost for cancel routing.
	pendingRequests sync.Map
}

// NewDownstreamService creates a DownstreamService.
func NewDownstreamService(
	reg *registry.Registry,
	agentTokens []string,
	upstream Upstream,
	proxyHostname string,
	logger *slog.Logger,
) *DownstreamService {
	return &DownstreamService{
		reg:           reg,
		agentTokens:   agentTokens,
		upstream:      upstream,
		proxyHostname: proxyHostname,
		logger:        logger,
	}
}

// Connect handles a bidirectional stream from a downstream agent or nested proxy.
func (s *DownstreamService) Connect(srv tunnel.TunnelService_ConnectServer) error {
	first, err := srv.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "recv first message: %v", err)
	}
	req := first.GetRegisterRequest()
	if req == nil {
		return status.Error(codes.InvalidArgument, "first message must be RegisterRequest")
	}

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

	rec := &registry.AgentRecord{
		Hostname:      req.Hostname,
		IP:            req.Ip,
		OS:            req.Os,
		AgentVersion:  req.AgentVersion,
		NodeType:      nodeTypeStr(req.NodeType),
		ProxyPath:     req.ProxyPath,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		Status:        registry.StatusOnline,
		RouteStream:   ts,
	}
	s.reg.Register(rec)
	s.logger.Info("downstream registered", "hostname", req.Hostname, "type", rec.NodeType)

	// Acknowledge downstream.
	if err := ts.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterAck{
			RegisterAck: &tunnel.RegisterAck{Success: true},
		},
	}); err != nil {
		s.reg.UnregisterByStream(ts)
		return err
	}

	// Forward a synthetic RegisterRequest upstream so center knows about this agent.
	upstreamPath := append([]string{s.proxyHostname}, req.ProxyPath...)
	if err := s.upstream.Send(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_RegisterRequest{
			RegisterRequest: &tunnel.RegisterRequest{
				Hostname:     req.Hostname,
				Ip:           req.Ip,
				Os:           req.Os,
				AgentVersion: req.AgentVersion,
				NodeType:     req.NodeType,
				ProxyPath:    upstreamPath,
				// Token is not forwarded — proxy uses its own token for the upstream stream.
			},
		},
	}); err != nil {
		s.logger.Warn("failed to forward registration upstream", "hostname", req.Hostname, "error", err)
	}

	defer func() {
		removed := s.reg.UnregisterByStream(ts)
		for _, h := range removed {
			s.logger.Info("downstream unregistered", "hostname", h)
		}
	}()

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
					HeartbeatAck: &tunnel.HeartbeatAck{TimestampMs: p.Heartbeat.TimestampMs},
				},
			})
			// 向 upstream (center) 转发一个 RegisterRequest，以更新 center 侧的 LastHeartbeat，
			// 防止 center 因长时间未收到心跳而将该 agent 标记为 offline。
			if hbRec := s.reg.Lookup(req.Hostname); hbRec != nil {
				upstreamPath := append([]string{s.proxyHostname}, hbRec.ProxyPath...)
				_ = s.upstream.Send(&tunnel.TunnelMessage{
					Payload: &tunnel.TunnelMessage_RegisterRequest{
						RegisterRequest: &tunnel.RegisterRequest{
							Hostname:     hbRec.Hostname,
							Ip:           hbRec.IP,
							Os:           hbRec.OS,
							AgentVersion: hbRec.AgentVersion,
							NodeType:     nodeTypeProto(hbRec.NodeType),
							ProxyPath:    upstreamPath,
						},
					},
				})
			}
		case *tunnel.TunnelMessage_ToolResponse:
			// Forward tool response upstream so center can deliver it.
			if resp := msg.GetToolResponse(); resp != nil {
				s.pendingRequests.Delete(resp.RequestId)
			}
			_ = s.upstream.Send(msg)
		case *tunnel.TunnelMessage_ErrorResponse:
			if resp := msg.GetErrorResponse(); resp != nil {
				s.pendingRequests.Delete(resp.RequestId)
			}
			_ = s.upstream.Send(msg)
		}
	}
}

// DeliverToolRequest routes a ToolRequest from upstream to the correct downstream stream.
// It records the request→targetHost mapping so CancelRequest can find the right stream.
func (s *DownstreamService) DeliverToolRequest(msg *tunnel.TunnelMessage) {
	req := msg.GetToolRequest()
	if req == nil {
		return
	}
	rec := s.reg.Lookup(req.TargetHost)
	if rec == nil {
		s.logger.Warn("target host not found in local registry", "host", req.TargetHost, "request_id", req.RequestId)
		_ = s.upstream.Send(&tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_ErrorResponse{
				ErrorResponse: &tunnel.ErrorResponse{
					RequestId: req.RequestId,
					Code:      "HOST_NOT_FOUND",
					Message:   "proxy: target host " + req.TargetHost + " not found",
				},
			},
		})
		return
	}
	// Track requestID -> targetHost for cancel routing.
	s.pendingRequests.Store(req.RequestId, req.TargetHost)
	if err := rec.RouteStream.Send(msg); err != nil {
		s.pendingRequests.Delete(req.RequestId)
		s.logger.Warn("failed to forward tool request to downstream", "host", req.TargetHost, "error", err)
	}
}

// DeliverCancelRequest forwards a CancelRequest from upstream to the downstream agent
// that is handling the corresponding tool request.
func (s *DownstreamService) DeliverCancelRequest(msg *tunnel.TunnelMessage) {
	cancel := msg.GetCancelRequest()
	if cancel == nil {
		return
	}
	v, ok := s.pendingRequests.LoadAndDelete(cancel.RequestId)
	if !ok {
		return // request already completed or not found
	}
	targetHost := v.(string)
	rec := s.reg.Lookup(targetHost)
	if rec == nil {
		return
	}
	if err := rec.RouteStream.Send(msg); err != nil {
		s.logger.Warn("failed to forward cancel to downstream", "host", targetHost, "request_id", cancel.RequestId, "error", err)
	}
}

// ReregisterAll forwards a RegisterRequest upstream for every currently online agent.
// Called after the upstream connection is re-established.
// pendingRequests is cleared first because all in-flight requests from the previous
// connection are now stale — the upstream will have already timed them out.
func (s *DownstreamService) ReregisterAll(ctx context.Context) {
	// 清除旧连接遗留的 pending 记录，防止内存泄漏
	s.pendingRequests.Range(func(k, _ any) bool {
		s.pendingRequests.Delete(k)
		return true
	})
	for _, rec := range s.reg.All() {
		if rec.Status != registry.StatusOnline {
			continue
		}
		upstreamPath := append([]string{s.proxyHostname}, rec.ProxyPath...)
		err := s.upstream.Send(&tunnel.TunnelMessage{
			Payload: &tunnel.TunnelMessage_RegisterRequest{
				RegisterRequest: &tunnel.RegisterRequest{
					Hostname:     rec.Hostname,
					Ip:           rec.IP,
					Os:           rec.OS,
					AgentVersion: rec.AgentVersion,
					NodeType:     nodeTypeProto(rec.NodeType),
					ProxyPath:    upstreamPath,
				},
			},
		})
		if err != nil {
			s.logger.Warn("re-register failed", "hostname", rec.Hostname, "error", err)
			return
		}
		s.logger.Info("re-registered agent upstream", "hostname", rec.Hostname)
	}
}

func (s *DownstreamService) validToken(token string) bool {
	token = strings.TrimPrefix(token, "Bearer ")
	for _, t := range s.agentTokens {
		if t == token {
			return true
		}
	}
	return false
}

func nodeTypeStr(nt tunnel.NodeType) string {
	if nt == tunnel.NodeType_NODE_TYPE_PROXY {
		return "proxy"
	}
	return "agent"
}

func nodeTypeProto(s string) tunnel.NodeType {
	if s == "proxy" {
		return tunnel.NodeType_NODE_TYPE_PROXY
	}
	return tunnel.NodeType_NODE_TYPE_AGENT
}
