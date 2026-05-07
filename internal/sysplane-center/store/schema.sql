-- sys-mcp-center 数据库模式
-- 版本：v1

CREATE TABLE IF NOT EXISTS agent_instances (
    hostname        TEXT        PRIMARY KEY,
    ip              TEXT        NOT NULL DEFAULT '',
    os              TEXT        NOT NULL DEFAULT '',
    agent_version   TEXT        NOT NULL DEFAULT '',
    node_type       TEXT        NOT NULL DEFAULT 'agent',
    proxy_path      TEXT[]      NOT NULL DEFAULT '{}',
    center_id       TEXT        NOT NULL DEFAULT '',
    status          TEXT        NOT NULL DEFAULT 'online'
                    CHECK (status IN ('online', 'offline')),
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS center_instances (
    instance_id      TEXT        PRIMARY KEY,
    internal_address TEXT        NOT NULL,
    last_heartbeat   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS tool_call_logs (
    id           BIGSERIAL   PRIMARY KEY,
    request_id   TEXT        NOT NULL UNIQUE,
    center_id    TEXT        NOT NULL DEFAULT '',
    target_host  TEXT        NOT NULL,
    tool_name    TEXT        NOT NULL,
    args_json    TEXT        NOT NULL DEFAULT '{}',
    result_json  TEXT,
    error_msg    TEXT,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agent_instances_status    ON agent_instances (status);
CREATE INDEX IF NOT EXISTS idx_agent_instances_center_id ON agent_instances (center_id);
CREATE INDEX IF NOT EXISTS idx_tool_call_logs_started_at ON tool_call_logs  (started_at DESC);
