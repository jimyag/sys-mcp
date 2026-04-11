package ha

import (
	"context"
	"errors"
	"fmt"

	"github.com/jimyag/sys-mcp/internal/sys-mcp-center/store"
)

// RouterBridge 在本地 registry 找不到 agent 时，
// 通过 PostgreSQL 查找 agent 所在的 center 实例并转发请求。
type RouterBridge struct {
	store      *store.Store
	instanceID string // 当前 center 实例 ID（本机不需要转发）
	secret     string // ha.internal_secret，传递给 ForwardToCenter
	useTLS     bool   // ha.internal_use_tls，内部转发是否使用 https
	skipVerify bool   // ha.internal_skip_verify，跳过 TLS 证书验证
}

// NewRouterBridge 创建跨实例路由桥。
func NewRouterBridge(st *store.Store, instanceID, secret string, useTLS, skipVerify bool) *RouterBridge {
	return &RouterBridge{
		store:      st,
		instanceID: instanceID,
		secret:     secret,
		useTLS:     useTLS,
		skipVerify: skipVerify,
	}
}

// ForwardIfNeeded 查询 PG 找到 targetHost 所在的 center 实例，然后转发工具请求。
// 如果 agent 在本实例，返回 (nil, false) 表示不需要转发。
func (b *RouterBridge) ForwardIfNeeded(ctx context.Context, requestID, targetHost, toolName, argsJSON string) (string, bool, error) {
	agentRow, err := b.store.GetAgent(ctx, targetHost)
	if errors.Is(err, store.ErrNotFound) {
		// agent 既不在本地 registry，也不在 PG 中，说明 agent 从未注册。
		// 返回 (false, nil) 表示"无需转发"，调用方继续走 not-found 分支。
		return "", false, nil
	}
	if err != nil {
		// PG 查询本身失败（连接断开、超时等），区别于"找不到 agent"。
		// 返回 forwarded=true 让调用方看到错误，而不是静默走 not-found 分支。
		return "", true, fmt.Errorf("ha: query agent %s: %w", targetHost, err)
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
	}, b.useTLS, b.skipVerify)
	if err != nil {
		return "", true, err
	}
	return result, true, nil
}
