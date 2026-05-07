// Package store 提供 sysplane-center 的 PostgreSQL 存储层。
package store

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by Get* methods when the requested record does not exist.
var ErrNotFound = errors.New("store: not found")

//go:embed schema.sql
var schemaDDL string

// Store 封装 PostgreSQL 连接池。
type Store struct {
	pool *pgxpool.Pool
}

// New 创建并初始化 Store。
func New(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: parse DSN: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: connect: %w", err)
	}
	// 验证连接可用性
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Migrate 执行 schema 迁移（幂等）。
func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, schemaDDL)
	return err
}

// Close 关闭连接池。
func (s *Store) Close() {
	s.pool.Close()
}

// Pool 返回底层连接池（供其他 store 方法使用）。
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}

// --- agent_instances 操作 ---

// AgentRow 对应 agent_instances 表的一行。
type AgentRow struct {
	Hostname      string
	IP            string
	OS            string
	AgentVersion  string
	NodeType      string
	ProxyPath     []string
	CenterID      string
	Status        string
	RegisteredAt  time.Time
	LastHeartbeat time.Time
}

// UpsertAgent 插入或更新 agent 记录。
func (s *Store) UpsertAgent(ctx context.Context, r *AgentRow) error {
	if r.ProxyPath == nil {
		r.ProxyPath = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agent_instances
			(hostname, ip, os, agent_version, node_type, proxy_path, center_id, status, registered_at, last_heartbeat)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (hostname) DO UPDATE SET
			ip             = EXCLUDED.ip,
			os             = EXCLUDED.os,
			agent_version  = EXCLUDED.agent_version,
			node_type      = EXCLUDED.node_type,
			proxy_path     = EXCLUDED.proxy_path,
			center_id      = EXCLUDED.center_id,
			status         = EXCLUDED.status,
			registered_at  = EXCLUDED.registered_at,
			last_heartbeat = EXCLUDED.last_heartbeat
	`, r.Hostname, r.IP, r.OS, r.AgentVersion, r.NodeType, r.ProxyPath,
		r.CenterID, r.Status, r.RegisteredAt, r.LastHeartbeat)
	return err
}

// UpdateAgentHeartbeat 更新 agent 心跳时间和在线状态。
func (s *Store) UpdateAgentHeartbeat(ctx context.Context, hostname string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE agent_instances
		SET last_heartbeat = now(), status = 'online'
		WHERE hostname = $1
	`, hostname)
	return err
}

// SetAgentOffline 将 agent 标记为离线。
func (s *Store) SetAgentOffline(ctx context.Context, hostname string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE agent_instances SET status = 'offline' WHERE hostname = $1
	`, hostname)
	return err
}

// DeleteAgent 删除 agent 记录。
func (s *Store) DeleteAgent(ctx context.Context, hostname string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM agent_instances WHERE hostname = $1`, hostname)
	return err
}

// GetAgent 按主机名查询 agent。
func (s *Store) GetAgent(ctx context.Context, hostname string) (*AgentRow, error) {
	row := &AgentRow{}
	err := s.pool.QueryRow(ctx, `
		SELECT hostname, ip, os, agent_version, node_type, proxy_path,
		       center_id, status, registered_at, last_heartbeat
		FROM agent_instances WHERE hostname = $1
	`, hostname).Scan(
		&row.Hostname, &row.IP, &row.OS, &row.AgentVersion, &row.NodeType,
		&row.ProxyPath, &row.CenterID, &row.Status, &row.RegisteredAt, &row.LastHeartbeat,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// --- center_instances 操作 ---

// UpsertCenter 注册或更新 center 实例。
func (s *Store) UpsertCenter(ctx context.Context, instanceID, internalAddress string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO center_instances (instance_id, internal_address, last_heartbeat)
		VALUES ($1, $2, now())
		ON CONFLICT (instance_id) DO UPDATE SET
			internal_address = EXCLUDED.internal_address,
			last_heartbeat   = now()
	`, instanceID, internalAddress)
	return err
}

// DeleteCenter 删除 center 实例记录（关闭时调用）。
func (s *Store) DeleteCenter(ctx context.Context, instanceID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM center_instances WHERE instance_id = $1`, instanceID)
	return err
}

// GetCenterAddress 查询指定 center 实例的内部地址。
func (s *Store) GetCenterAddress(ctx context.Context, instanceID string) (string, error) {
	var addr string
	err := s.pool.QueryRow(
		ctx,
		`SELECT internal_address FROM center_instances WHERE instance_id = $1`,
		instanceID,
	).Scan(&addr)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return addr, err
}

// --- tool_call_logs 操作 ---

// InsertToolCallLog 插入工具调用日志。
func (s *Store) InsertToolCallLog(ctx context.Context, requestID, centerID, targetHost, toolName, argsJSON string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tool_call_logs (request_id, center_id, target_host, tool_name, args_json)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (request_id) DO NOTHING
	`, requestID, centerID, targetHost, toolName, argsJSON)
	return err
}

// CompleteToolCallLog 更新工具调用结果。
func (s *Store) CompleteToolCallLog(ctx context.Context, requestID, resultJSON, errorMsg string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tool_call_logs
		SET result_json = $2, error_msg = $3, completed_at = now()
		WHERE request_id = $1
	`, requestID, resultJSON, errorMsg)
	return err
}
