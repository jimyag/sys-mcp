# sys-mcp 当前代码实现复审

## 复审结论

本轮针对上次确认的 4 个问题已经完成修复，并补了对应测试。当前这几个点不再构成阻塞项：

- center 间可路由地址已从监听地址中拆出，新增 `ha.internal_address`
- `tool_call_logs` 已按两段职责落库
- 首次注册已改为同步持久化，不再接受注册成功但库里还没记录的窗口
- 文件类工具的剩余 `ctx` 取消缺口已补齐

## 修复结果

1. center 实例发现地址与监听地址解耦

- `cmd/sys-mcp-center/main.go` 不再把 `listen.http_address` 直接写入 `center_instances.internal_address`
- `internal/sys-mcp-center/config/config.go` 新增 `ha.internal_address`，并在 `database.enable=true` 时强制要求配置
- 这样可以避免把 `:8080` 这类仅 bind 有意义、对其他 center 不可路由的地址写进数据库

2. `tool_call_logs` 明确区分“转发记录”和“执行记录”

- 入口 center 在决定跨 center 转发时先写一条转发侧日志，再在转发完成后补全结果
- 目标 center 在 `/internal/forward` 内部 handler 中再写一条实际执行日志，并在本地执行完成后补全结果
- 这样跨 center 请求会稳定留下两条记录，一条表示请求经由哪个 center 转发，一条表示最终由哪个 center 执行

3. 首次注册改为同步落库

- `internal/sys-mcp-center/tunnel_svc.go` 中首次 `RegisterRequest` 的 `UpsertAgent` 已收紧到同步路径
- 只有数据库写成功后才会把节点注册进内存 registry 并返回 `RegisterAck{Success:true}`
- 若持久化失败，会返回失败 ack，并以 `Unavailable` 结束注册链路
- 心跳和离线状态仍保持异步，这样只把真正影响跨 center 可发现性的首次注册做强一致收口

4. 文件类工具补齐取消语义

- `internal/sys-mcp-agent/fileops/readfile.go` 的 `readTail()` 已接收 `ctx`，扫描环形缓冲时会响应取消
- `internal/sys-mcp-agent/fileops/search.go` 在匹配循环、前后文拼装循环里都补了 `ctx.Done()` 检查

## 验证

新增测试覆盖了这次修复的关键语义：

- `internal/sys-mcp-center/config/config_test.go`
- `internal/sys-mcp-center/mcp/server_remote_log_test.go`
- `cmd/sys-mcp-center/main_test.go`
- `internal/sys-mcp-center/reregister_test.go`

已执行并通过：

```bash
go test ./internal/sys-mcp-center/config ./internal/sys-mcp-center/mcp ./internal/sys-mcp-center ./internal/sys-mcp-agent/fileops ./cmd/sys-mcp-center
go test ./...
```

## 当前判断

按这次复审范围看，前一轮保留的 4 个问题已经闭环。后续如果要继续挑设计层面的点，建议把注意力放到：

- `ha.internal_address` 的示例配置和部署文档是否同步补齐
- `tool_call_logs` 两类记录是否需要在 schema 或查询层明确区分用途
- 心跳/离线异步写库是否还需要补重试或告警指标
