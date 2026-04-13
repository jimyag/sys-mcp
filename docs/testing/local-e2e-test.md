# 本地端到端测试流程

本文档记录 sys-mcp 项目在单台开发机上完整运行四个服务并验证功能的操作步骤。测试拓扑覆盖：直连 agent、通过 proxy 的 agent、以及 MCP 客户端调用全链路。

---

## 测试拓扑

```
AI 客户端 (Python 测试脚本)
        │  HTTP SSE  (:18880)
        ▼
  sys-mcp-center  (:18890 gRPC, :18880 HTTP)
        │  gRPC 长连接
        ├──── sys-mcp-agent [agent-direct]      直连，hostname=agent-direct
        │
        └──── sys-mcp-proxy [proxy-idc1] (:18892 gRPC)
                    │  gRPC 长连接
                    └── sys-mcp-agent [agent-behind-proxy]  hostname=agent-behind-proxy
```

共三个节点注册到 center：`agent-direct`、`proxy-idc1`（proxy 本身）、`agent-behind-proxy`（经 proxy 注册）。

---

## 前置条件

- Go 1.21+（用于编译）
- Python 3.9+（用于测试脚本）
- 安装 Python 依赖：`pip3 install sseclient-py requests`

---

## 第一步：编译所有二进制

```bash
cd /path/to/sys-mcp
task build
# 或手动：
go build -o bin/sys-mcp-center ./cmd/sys-mcp-center
go build -o bin/sys-mcp-agent  ./cmd/sys-mcp-agent
go build -o bin/sys-mcp-proxy  ./cmd/sys-mcp-proxy
go build -o bin/sys-mcp-client ./cmd/sys-mcp-client
```

---

## 第二步：创建测试配置

```bash
mkdir -p /tmp/sys-mcp-test
```

### center.yaml

```yaml
listen:
  http_address: ":18880"
  grpc_address: ":18890"
auth:
  client_tokens:
    - "test-client-token"
  agent_tokens:
    - "test-agent-token"
router:
  request_timeout_sec: 10
logging:
  level: "debug"
  format: "json"
```

### agent-direct.yaml

```yaml
hostname: "agent-direct"
upstream:
  address: "127.0.0.1:18890"
  token: "test-agent-token"
tool_timeout_sec: 25
security:
  max_file_size_mb: 10
  blocked_paths:
    - /proc
    - /sys
    - /dev
logging:
  level: "info"
  format: "json"
```

### proxy.yaml

```yaml
hostname: "proxy-idc1"
listen:
  grpc_address: ":18892"
upstream:
  address: "127.0.0.1:18890"
  token: "test-agent-token"
auth:
  agent_tokens:
    - "test-proxy-agent-token"
logging:
  level: "info"
  format: "json"
```

### agent-behind-proxy.yaml

```yaml
hostname: "agent-behind-proxy"
upstream:
  address: "127.0.0.1:18892"
  token: "test-proxy-agent-token"
tool_timeout_sec: 25
security:
  max_file_size_mb: 10
  blocked_paths:
    - /proc
    - /sys
    - /dev
logging:
  level: "info"
  format: "json"
```

### client.yaml

```yaml
center:
  url: "http://127.0.0.1:18880"
  token: "test-client-token"
logging:
  level: "debug"
```

---

## 第三步：启动服务

每个命令在独立终端中运行，按顺序启动。

```bash
# 终端 1 — center（最先启动）
./bin/sys-mcp-center -config /tmp/sys-mcp-test/center.yaml

# 终端 2 — 直连 agent
./bin/sys-mcp-agent -config /tmp/sys-mcp-test/agent-direct.yaml

# 终端 3 — proxy
./bin/sys-mcp-proxy -config /tmp/sys-mcp-test/proxy.yaml

# 终端 4 — 经 proxy 的 agent
./bin/sys-mcp-agent -config /tmp/sys-mcp-test/agent-behind-proxy.yaml
```

**启动顺序说明**：center 必须最先启动；proxy 和 agent 可以在 center 就绪后以任意顺序启动，支持自动断线重连。

### 预期日志

center 启动后，直连 agent 注册时打印：

```
agent registered: hostname=agent-direct type=NODE_TYPE_AGENT
```

proxy 注册后打印：

```
agent registered: hostname=proxy-idc1 type=NODE_TYPE_PROXY
```

经 proxy 注册的 agent 打印：

```
agent registered: hostname=agent-behind-proxy type=NODE_TYPE_AGENT routeVia=proxy-idc1
```

---

## 第四步：运行测试脚本

将以下内容保存为 `/tmp/test_sys_mcp.py`：

```python
#!/usr/bin/env python3
"""sys-mcp 端到端测试脚本"""

import json
import threading
import time
import requests
import sseclient

BASE_URL = "http://127.0.0.1:18880"
TOKEN = "test-client-token"
HEADERS = {"Authorization": f"Bearer {TOKEN}"}

results = {}
lock = threading.Lock()

def call_tool(tool_name: str, arguments: dict) -> dict:
    """通过 MCP SSE 协议调用工具，返回结果。"""
    session_id = None
    tool_result = {"error": "timeout"}

    # 建立 SSE 连接
    sse_resp = requests.get(
        f"{BASE_URL}/sse", headers=HEADERS, stream=True, timeout=15
    )
    client = sseclient.SSEClient(sse_resp)

    def post_tool():
        nonlocal session_id
        # 等待 session 建立
        for _ in range(50):
            if session_id:
                break
            time.sleep(0.1)
        if not session_id:
            return

        payload = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": "tools/call",
            "params": {"name": tool_name, "arguments": arguments},
        }
        requests.post(
            f"{BASE_URL}/message?sessionId={session_id}",
            json=payload,
            headers=HEADERS,
            timeout=15,
        )

    threading.Thread(target=post_tool, daemon=True).start()

    for event in client.events():
        if event.event == "endpoint":
            # 从 endpoint 事件中提取 sessionId
            msg = event.data
            if "sessionId=" in msg:
                session_id = msg.split("sessionId=")[-1].strip()
        elif event.event == "message" and event.data:
            try:
                data = json.loads(event.data)
                if "result" in data or "error" in data:
                    tool_result = data
                    break
            except json.JSONDecodeError:
                pass

    sse_resp.close()
    return tool_result


def list_tools() -> list:
    """列出所有可用工具。"""
    session_id = None
    tools_result = []

    sse_resp = requests.get(
        f"{BASE_URL}/sse", headers=HEADERS, stream=True, timeout=15
    )
    client = sseclient.SSEClient(sse_resp)

    def post_list():
        nonlocal session_id
        for _ in range(50):
            if session_id:
                break
            time.sleep(0.1)
        if not session_id:
            return
        payload = {"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}
        requests.post(
            f"{BASE_URL}/message?sessionId={session_id}",
            json=payload,
            headers=HEADERS,
            timeout=10,
        )

    threading.Thread(target=post_list, daemon=True).start()

    for event in client.events():
        if event.event == "endpoint" and "sessionId=" in event.data:
            session_id = event.data.split("sessionId=")[-1].strip()
        elif event.event == "message" and event.data:
            try:
                data = json.loads(event.data)
                if "result" in data and "tools" in data.get("result", {}):
                    tools_result = data["result"]["tools"]
                    break
            except json.JSONDecodeError:
                pass

    sse_resp.close()
    return tools_result


def run_tests():
    passed = 0
    failed = 0

    def check(name, condition, detail=""):
        nonlocal passed, failed
        status = "PASS" if condition else "FAIL"
        marker = "✓" if condition else "✗"
        print(f"  [{status}] {name}")
        if detail:
            print(f"         {detail}")
        if condition:
            passed += 1
        else:
            failed += 1

    print("\n=== sys-mcp 端到端测试 ===\n")

    # 测试 1：list_tools
    print("[1] 获取工具列表")
    tools = list_tools()
    check("tools/list 返回工具", len(tools) >= 8, f"工具数: {len(tools)}")

    # 测试 2：list_agents
    print("[2] 查询已注册节点")
    r = call_tool("list_agents", {})
    content = r.get("result", {}).get("content", [{}])[0].get("text", "")
    agents = json.loads(content) if content else []
    hostnames = {a.get("hostname") for a in agents}
    check(
        "list_agents 返回 3 个节点",
        len(agents) >= 3,
        f"节点: {hostnames}",
    )
    check("agent-direct 在线", "agent-direct" in hostnames)
    check("proxy-idc1 在线", "proxy-idc1" in hostnames)
    check("agent-behind-proxy 在线", "agent-behind-proxy" in hostnames)

    # 测试 3：直连 agent 的硬件信息
    print("[3] 直连 agent 硬件信息")
    r = call_tool("get_hardware_info", {"target_host": "agent-direct"})
    text = r.get("result", {}).get("content", [{}])[0].get("text", "")
    check("get_hardware_info 返回非空内容", bool(text), text[:80] if text else "")

    # 测试 4：直连 agent 目录列表
    print("[4] 直连 agent 目录列表 /etc")
    r = call_tool("list_directory", {"target_host": "agent-direct", "path": "/etc"})
    text = r.get("result", {}).get("content", [{}])[0].get("text", "")
    items = json.loads(text) if text else []
    check("list_directory /etc 返回条目", len(items) > 0, f"条目数: {len(items)}")

    # 测试 5：直连 agent stat 文件
    print("[5] 直连 agent stat /etc/hosts")
    r = call_tool("stat_file", {"target_host": "agent-direct", "path": "/etc/hosts"})
    text = r.get("result", {}).get("content", [{}])[0].get("text", "")
    info = json.loads(text) if text else {}
    check("stat_file /etc/hosts 返回 size", "size" in info, f"size={info.get('size')}")

    # 测试 6：经 proxy 的 agent 硬件信息
    print("[6] 经 proxy 的 agent 硬件信息")
    r = call_tool("get_hardware_info", {"target_host": "agent-behind-proxy"})
    text = r.get("result", {}).get("content", [{}])[0].get("text", "")
    check("get_hardware_info (via proxy) 返回非空", bool(text), text[:80] if text else "")

    # 测试 7：check_path_exists
    print("[7] check_path_exists /etc/hosts")
    r = call_tool(
        "check_path_exists",
        {"target_host": "agent-direct", "path": "/etc/hosts"},
    )
    text = r.get("result", {}).get("content", [{}])[0].get("text", "")
    info = json.loads(text) if text else {}
    check("check_path_exists 返回 exists=true", info.get("exists") is True)

    print(f"\n结果：{passed} 通过，{failed} 失败\n")
    return failed == 0


if __name__ == "__main__":
    import sys
    success = run_tests()
    sys.exit(0 if success else 1)
```

执行：

```bash
python3 /tmp/test_sys_mcp.py
```

---

## 预期输出

```
=== sys-mcp 端到端测试 ===

[1] 获取工具列表
  [PASS] tools/list 返回工具
         工具数: 8
[2] 查询已注册节点
  [PASS] list_agents 返回 3 个节点
         节点: {'agent-direct', 'proxy-idc1', 'agent-behind-proxy'}
  [PASS] agent-direct 在线
  [PASS] proxy-idc1 在线
  [PASS] agent-behind-proxy 在线
[3] 直连 agent 硬件信息
  [PASS] get_hardware_info 返回非空内容
         {"cpu":{"model":"Apple M1 Pro",...
[4] 直连 agent 目录列表 /etc
  [PASS] list_directory /etc 返回条目
         条目数: 81
[5] 直连 agent stat /etc/hosts
  [PASS] stat_file /etc/hosts 返回 size
         size=1536
[6] 经 proxy 的 agent 硬件信息
  [PASS] get_hardware_info (via proxy) 返回非空
         {"cpu":{"model":"Apple M1 Pro",...
[7] check_path_exists /etc/hosts
  [PASS] check_path_exists 返回 exists=true

结果：10 通过，0 失败
```

---

## 常见问题

### agent 无法注册

- 检查 center 是否已启动，gRPC 端口（默认 `:18890`）是否可达。
- 检查 `upstream.token` 是否与 center 的 `auth.agent_tokens` 中的值一致。

### 工具调用超时

- 检查对应 agent/proxy 是否在线（先调用 `list_agents` 确认 `status=online`）。
- 适当增大 `router.request_timeout_sec`（center 配置）或 `tool_timeout_sec`（agent 配置）。

### 同一台机器上多个 agent 的 hostname 冲突

- 必须在每个 agent/proxy 的配置文件中显式设置 `hostname` 字段，否则所有进程都使用 `os.Hostname()` 返回的相同值，导致注册互相覆盖。

### InputSchema panic

- `mcp.AddTool` 要求每个工具都必须有非空的 `InputSchema`，即使该工具没有参数也需要设置 `InputSchema: &jsonschema.Schema{Type: "object"}`。

---

## 自动化运行（Taskfile）

```bash
# 编译 + 单元测试 + vet
task test

# 仅编译
task build
```
