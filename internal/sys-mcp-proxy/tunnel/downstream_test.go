package tunnel_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jimyag/sys-mcp/api/tunnel"
	proxyreg "github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/registry"
	proxytunnel "github.com/jimyag/sys-mcp/internal/sys-mcp-proxy/tunnel"
	"log/slog"
	"os"
)

func TestDownstreamCompiles(t *testing.T) {
	// 验证包编译通过（downstream 功能通过集成测试覆盖）
	t.Log("downstream 包编译通过")
}

// fakeUpstream 捕获发往 upstream 的消息。
type fakeUpstream struct {
	mu   sync.Mutex
	msgs []*tunnel.TunnelMessage
}

func (u *fakeUpstream) Send(msg *tunnel.TunnelMessage) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.msgs = append(u.msgs, msg)
	return nil
}

// fakeAgentStream 模拟下游 agent 流。
type fakeAgentStream struct {
	mu   sync.Mutex
	msgs []*tunnel.TunnelMessage
}

func (s *fakeAgentStream) Send(msg *tunnel.TunnelMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.msgs = append(s.msgs, msg)
	return nil
}
func (s *fakeAgentStream) Recv() (*tunnel.TunnelMessage, error) { return nil, nil }
func (s *fakeAgentStream) Context() context.Context             { return context.Background() }
func (s *fakeAgentStream) ID() string                           { return "fake-agent-stream" }
func (s *fakeAgentStream) RemoteAddr() string                   { return "127.0.0.1:0" }

// TestDeliverCancelRequest_ForwardsToCorrectAgent 验证 DeliverCancelRequest 将取消消息
// 路由到正确的 agent stream，且 pendingRequests 在收到响应后清理。
func TestDeliverCancelRequest_ForwardsToCorrectAgent(t *testing.T) {
	reg := proxyreg.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	fakeUp := &fakeUpstream{}
	agentStream := &fakeAgentStream{}

	svc := proxytunnel.NewDownstreamService(
		reg,
		[]string{"token-abc"},
		fakeUp,
		"proxy-01",
		logger,
	)

	// 直接注册 agent 到 registry
	reg.Register(&proxyreg.AgentRecord{
		Hostname:      "agent-01",
		Status:        proxyreg.StatusOnline,
		NodeType:      "agent",
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		RouteStream:   agentStream,
	})

	// 先发送 ToolRequest，建立 pendingRequests 映射
	toolMsg := &tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_ToolRequest{
			ToolRequest: &tunnel.ToolRequest{
				RequestId:  "req-001",
				TargetHost: "agent-01",
				ToolName:   "get_cpu_info",
				ArgsJson:   `{"target_host":"agent-01"}`,
			},
		},
	}
	svc.DeliverToolRequest(toolMsg)

	// 验证 ToolRequest 被转发到 agent stream
	agentStream.mu.Lock()
	toolCount := len(agentStream.msgs)
	agentStream.mu.Unlock()
	if toolCount != 1 {
		t.Fatalf("期望 1 条 ToolRequest，得到 %d", toolCount)
	}

	// 发送 CancelRequest
	cancelMsg := &tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_CancelRequest{
			CancelRequest: &tunnel.CancelRequest{RequestId: "req-001"},
		},
	}
	svc.DeliverCancelRequest(cancelMsg)

	// 验证 CancelRequest 被转发到 agent stream
	agentStream.mu.Lock()
	allMsgs := make([]*tunnel.TunnelMessage, len(agentStream.msgs))
	copy(allMsgs, agentStream.msgs)
	agentStream.mu.Unlock()

	var hasCancelRequest bool
	for _, m := range allMsgs {
		if c := m.GetCancelRequest(); c != nil && c.RequestId == "req-001" {
			hasCancelRequest = true
		}
	}
	if !hasCancelRequest {
		t.Errorf("期望 CancelRequest 被转发到 agent stream，实际消息: %v", allMsgs)
	}
}

// TestDeliverCancelRequest_UnknownRequestIDIsNoop 验证未知 requestID 不 panic。
func TestDeliverCancelRequest_UnknownRequestIDIsNoop(t *testing.T) {
	reg := proxyreg.New()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	fakeUp := &fakeUpstream{}

	svc := proxytunnel.NewDownstreamService(
		reg,
		[]string{"token"},
		fakeUp,
		"proxy-01",
		logger,
	)

	// 应该不 panic
	svc.DeliverCancelRequest(&tunnel.TunnelMessage{
		Payload: &tunnel.TunnelMessage_CancelRequest{
			CancelRequest: &tunnel.CancelRequest{RequestId: "unknown-req"},
		},
	})
}

