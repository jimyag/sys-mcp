# sys-mcp 端到端集成测试验证报告

测试日期：2026-04-11  
测试环境：macOS Darwin 25.3.0 (Apple M1 Pro)  
测试结果：**37 / 37 通过，0 失败**

---

## 拓扑结构

```
AI Client (MCP SSE)
       |
   center :18880 (HTTP/MCP)
       |      |
       |   :18890 (gRPC)
       |      |
   agent-1    proxy-1 :18892 (gRPC)
(test-agent-1)    |
            agent-2
          (test-agent-2)
```

Python HTTP 服务器运行在 `localhost:18888`，供 `proxy_local_api` 工具使用。

---

## 服务配置

| 组件         | 监听地址      | 连接上游          | 主机名          |
|--------------|--------------|-------------------|----------------|
| center       | :18880 (HTTP), :18890 (gRPC) | PostgreSQL :5432 | — |
| agent-1      | —            | center :18890     | test-agent-1   |
| proxy-1      | :18892 (gRPC) | center :18890    | test-proxy-1   |
| agent-2      | —            | proxy-1 :18892    | test-agent-2   |

---

## 测试用例明细

### 鉴权 (TC-01 ~ TC-03)

| ID     | 测试场景                           | 结果 |
|--------|------------------------------------|------|
| TC-01  | 无 Token 访问 SSE 被拒绝 (HTTP 401) | PASS |
| TC-02  | 错误 Token 被拒绝 (HTTP 401)        | PASS |
| TC-03  | 正确 Token 连接 center 成功         | PASS |

### 工具发现 (TC-04 ~ TC-05)

| ID     | 测试场景                           | 结果 | 说明 |
|--------|------------------------------------|------|------|
| TC-04  | list_tools 返回工具列表 (≥9 个)    | PASS | 实际返回 10 个工具 |
| TC-05  | list_agents 返回在线 agent 列表    | PASS | total=2, online=2 |

### 文件操作 — test-agent-1 直连 (TC-06 ~ TC-15)

| ID     | 测试场景                                     | 结果 |
|--------|----------------------------------------------|------|
| TC-06  | list_directory 正常路径返回文件列表           | PASS |
| TC-07  | list_directory 访问 /proc 被 blocked_paths 拦截 | PASS |
| TC-08  | stat_file 返回 size/permissions/type         | PASS |
| TC-09  | check_path_exists 已存在文件返回 exists:true  | PASS |
| TC-10  | check_path_exists 不存在文件返回 exists:false | PASS |
| TC-11  | read_file 全量读取文本内容正确               | PASS |
| TC-12  | read_file head=2 返回前 2 行且 truncated=true | PASS |
| TC-13  | read_file tail=2 返回最后 2 行含 WARNING      | PASS |
| TC-14  | read_file 超过 max_file_size_mb(10MB) 被拒绝  | PASS |
| TC-15  | read_file 二进制文件返回 is_binary:true       | PASS |

### 文件搜索 (TC-16 ~ TC-22)

| ID     | 测试场景                                        | 结果 |
|--------|-------------------------------------------------|------|
| TC-16  | search 大小写敏感匹配 ERROR (1 条)              | PASS |
| TC-17  | search ignore_case=true 匹配 error/ERROR        | PASS |
| TC-18  | search invert_match 返回非 ERROR 行 (4 条)      | PASS |
| TC-19  | search count_only=true 返回计数且不含 matches   | PASS |
| TC-20  | search context ±1 行返回 match+前+后共 3 条     | PASS |
| TC-21  | search fixed_string 匹配字面量含特殊字符        | PASS |
| TC-22  | search 无效正则返回 isError                     | PASS |

### 硬件信息 (TC-23)

| ID     | 测试场景                                        | 结果 |
|--------|-------------------------------------------------|------|
| TC-23  | get_hardware_info 返回 cpu/memory/disk/network/system | PASS |

### 本地 HTTP 代理 (TC-24 ~ TC-26)

| ID     | 测试场景                                      | 结果 |
|--------|-----------------------------------------------|------|
| TC-24  | proxy_local_api GET /hello.txt 返回 200 及内容 | PASS |
| TC-25  | proxy_local_api 端口 9999 不在白名单被拒绝    | PASS |
| TC-26  | proxy_local_api 特权端口 80 被拒绝            | PASS |

### 经 proxy 路由到 agent-2 (TC-27 ~ TC-29)

| ID     | 测试场景                                        | 结果 |
|--------|-------------------------------------------------|------|
| TC-27  | list_directory 经 proxy 路由到 test-agent-2     | PASS |
| TC-28  | get_hardware_info 经 proxy 路由到 test-agent-2  | PASS |
| TC-29  | read_file 经 proxy 路由到 test-agent-2 内容正确 | PASS |

### 多机并发工具 (TC-30 ~ TC-32)

| ID     | 测试场景                                         | 结果 |
|--------|--------------------------------------------------|------|
| TC-30  | get_hardware_info_multi 空 hosts 覆盖所有在线 agent | PASS |
| TC-31  | list_directory_multi 指定 2 台均有独立结果       | PASS |
| TC-32  | multi 含不存在 host 时该 host 返回 error 字段    | PASS |

### 边界与安全场景 (TC-33 ~ TC-36)

| ID     | 测试场景                                           | 结果 |
|--------|----------------------------------------------------|------|
| TC-33  | 调用不存在的 agent 返回 isError                    | PASS |
| TC-34  | 缺少 target_host 参数返回 isError                  | PASS |
| TC-35  | symlink 指向 /etc/passwd（不在 blocked_paths）可读取 | PASS |
| TC-36  | symlink 指向 /dev/null（在 blocked_paths=/dev）被拒绝 | PASS |

### 重连机制 (TC-37)

| ID     | 测试场景                                        | 结果 |
|--------|-------------------------------------------------|------|
| TC-37  | agent 断线后指数退避重连（informational）        | PASS |

---

## 关键验证结论

1. **鉴权**：Bearer token 校验在 SSE 端点生效，无 token 或错误 token 均返回 401。

2. **直连路由**：center 能正确将工具请求路由到直连的 test-agent-1，文件操作、搜索、硬件信息均正常。

3. **经 proxy 路由**：center → proxy-1 → agent-2 三层转发链路正常，工具结果完整返回。

4. **安全边界 (PathGuard)**：
   - blocked_paths 精确拦截 `/proc`、`/sys`、`/dev` 及其子路径
   - symlink 目标路径同样受 blocked_paths 约束（/dev/null 被拒）
   - symlink 指向不受限路径（/etc/passwd）可正常读取
   - 特权端口（<1024）和未授权端口均被 proxy_local_api 拒绝

5. **文件操作**：超过大小限制被拒，二进制文件返回 is_binary 标志而不报错，head/tail 截断正确。

6. **多机并发**：`_multi` 工具并发调用所有在线 agent，不存在的 host 返回独立的 error 字段而不影响其他主机结果。

7. **HA nil-interface 修复**：未配置 PostgreSQL 时，`RouterBridge` 为 nil，正确包装为 nil `RemoteForwarder` 接口，避免了将 nil 具体类型赋给接口导致的 nil pointer panic。此修复使 TC-32 的 ghost-host 分支和 TC-33～36 的边界测试均能稳定运行。

---

## 测试程序位置

测试程序：`/tmp/sys-mcp-test/e2e_main.go`（不在代码仓库中，仅用于本地验证）

运行方式：
```bash
# 启动所有服务（参见上方拓扑）后执行：
cd /tmp/sys-mcp-test && go run e2e_main.go
```
