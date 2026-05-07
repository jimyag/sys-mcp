package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jimyag/sysplane/internal/sysplane-center/store"
)

// TestStoreIntegration 需要本地 PostgreSQL，通过 TEST_PG_DSN 环境变量指定。
// 若未设置，跳过测试。
func TestStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/sys_mcp_test"
	}
	// 尝试连接，若失败则跳过
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	st, err := store.New(ctx, dsn, 5)
	if err != nil {
		t.Skipf("跳过 PG 集成测试（无法连接 %s）: %v", dsn, err)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migration 失败: %v", err)
	}

	// 测试 agent CRUD
	agent := &store.AgentRow{
		Hostname:      "test-host-01",
		IP:            "192.168.1.1",
		OS:            "linux/amd64",
		AgentVersion:  "0.1.0",
		NodeType:      "agent",
		ProxyPath:     []string{},
		CenterID:      "center-01",
		Status:        "online",
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
	}

	if err := st.UpsertAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertAgent 失败: %v", err)
	}

	got, err := st.GetAgent(ctx, "test-host-01")
	if err != nil {
		t.Fatalf("GetAgent 失败: %v", err)
	}
	if got.Hostname != "test-host-01" {
		t.Errorf("期望 hostname=test-host-01，得到 %s", got.Hostname)
	}
	if got.CenterID != "center-01" {
		t.Errorf("期望 center_id=center-01，得到 %s", got.CenterID)
	}

	// 测试心跳更新
	if err := st.UpdateAgentHeartbeat(ctx, "test-host-01"); err != nil {
		t.Fatalf("UpdateAgentHeartbeat 失败: %v", err)
	}

	// 测试标记离线
	if err := st.SetAgentOffline(ctx, "test-host-01"); err != nil {
		t.Fatalf("SetAgentOffline 失败: %v", err)
	}
	got, _ = st.GetAgent(ctx, "test-host-01")
	if got.Status != "offline" {
		t.Errorf("期望 status=offline，得到 %s", got.Status)
	}

	// 清理
	if err := st.DeleteAgent(ctx, "test-host-01"); err != nil {
		t.Fatalf("DeleteAgent 失败: %v", err)
	}

	// 测试 center 注册
	if err := st.UpsertCenter(ctx, "center-01", "localhost:9091"); err != nil {
		t.Fatalf("UpsertCenter 失败: %v", err)
	}
	addr, err := st.GetCenterAddress(ctx, "center-01")
	if err != nil {
		t.Fatalf("GetCenterAddress 失败: %v", err)
	}
	if addr != "localhost:9091" {
		t.Errorf("期望 addr=localhost:9091，得到 %s", addr)
	}
	_ = st.DeleteCenter(ctx, "center-01")

	// 测试 tool_call_logs
	if err := st.InsertToolCallLog(ctx, "req-001", "center-01", "test-host-01", "get_hardware_info", "{}"); err != nil {
		t.Fatalf("InsertToolCallLog 失败: %v", err)
	}
	if err := st.CompleteToolCallLog(ctx, "req-001", `{"cpu":"ok"}`, ""); err != nil {
		t.Fatalf("CompleteToolCallLog 失败: %v", err)
	}
}
