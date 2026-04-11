package mcp_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

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

// TestListAgents_ProxyFiltered 通过真实的 MCP 服务端（InMemoryTransport）
// 调用 list_agents 工具，验证 proxy 节点不出现在结果中。
func TestListAgents_ProxyFiltered(t *testing.T) {
	reg := buildReg()
	rtr := router.New(5)

	// 使用内存传输构造服务端与客户端
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()

	srv := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "test", Version: "0.0.1"},
		nil,
	)
	// 只注册 list_agents（无需 agent proxy 工具），复用 NewMCPHandler 内部的 buildServer 逻辑
	// 这里通过创建完整 handler 后提取出 server；由于 buildServer 是包内私有函数，
	// 我们直接在 test 内构造一个 NewMCPHandler 并通过 InMemoryTransport 验证行为。
	_ = srv
	_ = serverTransport

	// 使用 NewMCPHandler 创建完整 handler（含 list_agents tool），
	// 通过 sdk server 的 Run + InMemoryTransport 在 goroutine 中运行。
	sdkSrv := buildMCPServer(reg, rtr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 在后台运行 MCP server（单次连接）
	errCh := make(chan error, 1)
	go func() {
		errCh <- sdkSrv.Run(ctx, serverTransport)
	}()

	// 连接客户端
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect failed: %v", err)
	}
	defer cs.Close()

	// 调用 list_agents
	result, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{Name: "list_agents"})
	if err != nil {
		t.Fatalf("CallTool list_agents failed: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("期望 list_agents 返回非空内容")
	}

	text, ok := result.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("期望 TextContent，得到 %T", result.Content[0])
	}

	// 解析结果
	var got struct {
		Agents []struct {
			Hostname string `json:"hostname"`
			NodeType string `json:"node_type"`
		} `json:"agents"`
		Total  int `json:"total"`
		Online int `json:"online"`
	}
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("unmarshal result: %v\nraw: %s", err, text.Text)
	}

	if got.Total != 1 {
		t.Errorf("期望 Total=1（排除 proxy），得到 %d", got.Total)
	}
	if got.Online != 1 {
		t.Errorf("期望 Online=1，得到 %d", got.Online)
	}
	for _, a := range got.Agents {
		if a.NodeType == "proxy" {
			t.Errorf("list_agents 结果中包含了 proxy 节点: %s", a.Hostname)
		}
	}

	// 关闭客户端后等待 server 退出
	cs.Close()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
	}
}

// buildMCPServer 创建用于测试的内部 sdkmcp.Server，直接包含 list_agents 等工具。
// 这里复用 mcp 包的 NewMCPHandler 所包含的逻辑（通过 NewMCPHandler 创建的 handler
// 内嵌了相同的 sdkmcp.Server），不使用 SSE 层。
func buildMCPServer(reg *registry.Registry, rtr *router.Router) *sdkmcp.Server {
	// 使用 NewMCPHandler 并将 handler 包装为 HTTP 测试服务，
	// 但为了避免 SSE 解析复杂度，直接用 InMemoryTransport 测试。
	// 因此这里重新调用包级公开入口，并让 SDK server 自身处理协议。
	//
	// 注意：mcp.NewMCPHandler 返回的是 http.Handler（SSE），
	// 而 sdkmcp.Server 可以直接通过 Run/Connect 运行。
	// mcp.NewMCPHandler 内部已调用 buildServer；我们通过
	// 测试 helper 直接构建 sdkmcp.Server（与生产路径完全相同）。
	return mcp.BuildServerForTest(reg, rtr, nil, nil, "test-instance")
}
