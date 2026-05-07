// Package ha 提供 sysplane-center 的高可用支持：center 实例自注册和跨实例路由。
package ha

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jimyag/sysplane/internal/sysplane-center/store"
)

// CenterRegistrar 负责将当前 center 实例注册到 PostgreSQL 并维持心跳。
type CenterRegistrar struct {
	store           *store.Store
	instanceID      string
	internalAddress string
	logger          *slog.Logger
}

// NewCenterRegistrar 创建注册器。
// instanceID 建议使用 hostname:grpc_port。
// internalAddress 是其他 center 实例调用本实例内部 API 的 HTTP 地址。
func NewCenterRegistrar(st *store.Store, instanceID, internalAddress string, logger *slog.Logger) *CenterRegistrar {
	return &CenterRegistrar{
		store:           st,
		instanceID:      instanceID,
		internalAddress: internalAddress,
		logger:          logger,
	}
}

// Start 立刻注册当前实例，然后在后台每 30 秒刷新心跳，直到 ctx 取消。
// 取消时自动删除注册记录。
func (r *CenterRegistrar) Start(ctx context.Context) error {
	if err := r.store.UpsertCenter(ctx, r.instanceID, r.internalAddress); err != nil {
		return fmt.Errorf("ha: register center: %w", err)
	}
	r.logger.Info("center 实例已注册", "instance_id", r.instanceID, "internal_address", r.internalAddress)

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// 关闭时注销
				delCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := r.store.DeleteCenter(delCtx, r.instanceID); err != nil {
					r.logger.Warn("center 实例注销失败", "error", err)
				} else {
					r.logger.Info("center 实例已注销", "instance_id", r.instanceID)
				}
				return
			case <-ticker.C:
				func() {
					hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := r.store.UpsertCenter(hbCtx, r.instanceID, r.internalAddress); err != nil {
						r.logger.Warn("center 心跳更新失败", "error", err)
					}
				}()
			}
		}
	}()
	return nil
}

// InstanceID 返回当前实例 ID。
func (r *CenterRegistrar) InstanceID() string {
	return r.instanceID
}
