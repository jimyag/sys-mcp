package ha

import (
	"context"
	"fmt"

	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/store"
)

// RouterBridge 在本地 registry 找不到 agent 时，
// 通过 PostgreSQL 查找 agent 所在的 center 实例并转发请求。
type RouterBridge struct {
	store      *store.Store
	instanceID string // 当前 center 实例 ID（本机不需要转发）
	secret     string // ha.internal_secret，传递给 ForwardToCenter
}

// NewRouterBridge 创建跨实例路由桥。
func NewRouterBridge(st *store.Store, instanceID, secret string) *RouterBridge {
	return &RouterBridge{store: st, instanceID: instanceID, secret: secret}
}

// ForwardIfNeeded 查询 PG 找到 targetHost 所在的 center 实例，然后转发工具请求。
// 如果 agent 在本实例，返回 (nil, false) 表示不需要转发。
func (b *RouterBridge) ForwardIfNeeded(ctx context.Context, requestID, targetHost, toolName, argsJSON string) (string, bool, error) {
	agentRow, err := b.store.GetAgent(ctx, targetHost)
	if err != nil {
		// agent 不在 PG 中，也不在本地，说明 agent 未注册
		return "", false, fmt.Errorf("ha: agent %s not found in database", targetHost)
	}

	if agentRow.CenterID == b.instanceID {
		// agent 在本实例，由调用方处理
		return "", false, nil
	}

	// 查找目标 center 实例的地址
	targetAddr, err := b.store.GetCenterAddress(ctx, agentRow.CenterID)
	if err != nil {
		return "", false, fmt.Errorf("ha: center instance %s address not found: %w", agentRow.CenterID, err)
	}

	result, err := ForwardToCenter(ctx, targetAddr, b.secret, ForwardRequest{
		RequestID:  requestID,
		ToolName:   toolName,
		ArgsJSON:   argsJSON,
		TargetHost: targetHost,
	})
	if err != nil {
		return "", true, err
	}
	return result, true, nil
}
