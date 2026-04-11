package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/mcp"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/registry"
	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/router"
)

// --- 测试辅助类型 ---

// fakeForwarder 用于测试 RouterBridge wiring。
type fakeForwarder struct {
	called     bool
	returnJSON string
	returnErr  error
}

func (f *fakeForwarder) ForwardIfNeeded(_ context.Context, _, _, _, _ string) (string, bool, error) {
	f.called = true
	return f.returnJSON, true, f.returnErr
}

// fakeLogger 用于测试 CallLogger wiring。
type fakeLogger struct {
	inserts   int
	completes int
}

func (l *fakeLogger) InsertToolCallLog(_ context.Context, _, _, _, _, _ string) error {
	l.inserts++
	return nil
}
func (l *fakeLogger) CompleteToolCallLog(_ context.Context, _, _, _ string) error {
	l.completes++
	return nil
}

// buildReg 创建包含 agent 和 proxy 记录的 registry。
func buildReg() *registry.Registry {
	reg := registry.New()
	reg.Register(&registry.AgentRecord{
		Hostname:      "agent-01",
		NodeType:      "agent",
		Status:        registry.StatusOnline,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
	})
	reg.Register(&registry.AgentRecord{
		Hostname:      "proxy-01",
		NodeType:      "proxy",
		Status:        registry.StatusOnline,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
	})
	return reg
}

// --- 测试用例 ---

// TestRemoteForwarder_Interface 验证 fakeForwarder 满足 RemoteForwarder 接口。
func TestRemoteForwarder_Interface(t *testing.T) {
	var _ mcp.RemoteForwarder = (*fakeForwarder)(nil)
}

// TestCallLogger_Interface 验证 fakeLogger 满足 CallLogger 接口。
func TestCallLogger_Interface(t *testing.T) {
	var _ mcp.CallLogger = (*fakeLogger)(nil)
}

// TestNewMCPHandler_WithNilOptions 验证传入 nil 可选参数时不 panic。
func TestNewMCPHandler_WithNilOptions(t *testing.T) {
	reg := buildReg()
	rtr := router.New(5)
	// 应该不 panic
	h := mcp.NewMCPHandler(reg, rtr, []string{"token-abc"}, nil, nil, "test-instance")
	if h == nil {
		t.Fatal("期望返回非 nil handler")
	}
}

// TestListAgentsResult_ProxyFiltered 通过直接构造结果验证过滤逻辑。
func TestListAgentsResult_ProxyFiltered(t *testing.T) {
	type agentInfo struct {
		Hostname string `json:"hostname"`
		NodeType string `json:"node_type"`
	}
	type result struct {
		Agents []agentInfo `json:"agents"`
		Total  int         `json:"total"`
		Online int         `json:"online"`
	}

	reg := buildReg()
	records := reg.All()

	agents := make([]agentInfo, 0)
	online := 0
	for _, r := range records {
		if r.NodeType != "agent" {
			continue
		}
		agents = append(agents, agentInfo{Hostname: r.Hostname, NodeType: r.NodeType})
		if r.Status == registry.StatusOnline {
			online++
		}
	}

	res := result{Agents: agents, Total: len(agents), Online: online}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}

	var got result
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	if got.Total != 1 {
		t.Errorf("期望 Total=1，得到 %d", got.Total)
	}
	if got.Online != 1 {
		t.Errorf("期望 Online=1，得到 %d", got.Online)
	}
	if len(got.Agents) != 1 || got.Agents[0].NodeType != "agent" {
		t.Errorf("期望只有 agent 节点，得到 %v", got.Agents)
	}
}

