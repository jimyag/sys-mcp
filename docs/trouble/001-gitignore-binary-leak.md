# 故障记录 001：.gitignore 规则导致二进制文件泄露到 git 历史

## 时间

2026-04-11

## 现象

1. 首次提交时，`sys-mcp-agent` 二进制文件（约 18 MB）被意外提交到 `feat(agent): add Agent struct and cmd entry point` 这个 commit，并推送到了 GitHub。
2. 后续通过 `chore: add .gitignore and remove tracked binary` 在工作区删除了该文件，但二进制内容依然保留在 git 对象库（历史）中。
3. `internal/sys-mcp-center/`、`internal/sys-mcp-proxy/`、`internal/sys-mcp-client/`、`cmd/sys-mcp-center/`、`cmd/sys-mcp-proxy/`、`cmd/sys-mcp-client/` 目录下的所有源文件从未被 git 追踪——实现代码无法推送到仓库。

## 根本原因

`.gitignore` 中写的是：

```
sys-mcp-agent
sys-mcp-center
sys-mcp-proxy
sys-mcp-client
```

git 将不带 `/` 前缀的模式视为**全路径通配**：匹配任意目录层级中含有该名称的路径段，而不仅仅是仓库根目录下的文件。因此：

- `sys-mcp-agent` 匹配了 `internal/sys-mcp-agent/` 下的所有文件（源码被忽略）。
- 同理，`sys-mcp-center`、`sys-mcp-proxy`、`sys-mcp-client` 分别忽略了对应目录的所有内容。
- 二进制文件在 `.gitignore` 生效前已被 `git add` 追踪，所以没有被忽略，反而进入了提交。

## 影响

- GitHub 仓库历史中存留了约 18 MB 的 ELF 二进制，任何 `git clone` 都会下载该数据。
- center / proxy / client 三个服务的所有实现代码丢失在本地，未提交到远端。

## 修复步骤

### 1. 修正 .gitignore

将根级别二进制排除规则加上 `/` 前缀，限定只匹配仓库根目录：

```diff
-sys-mcp-agent
-sys-mcp-proxy
-sys-mcp-center
-sys-mcp-client
+/sys-mcp-agent
+/sys-mcp-proxy
+/sys-mcp-center
+/sys-mcp-client
```

### 2. 补提源码文件

修正后执行：

```bash
git add cmd/sys-mcp-center cmd/sys-mcp-client cmd/sys-mcp-proxy \
        internal/sys-mcp-center internal/sys-mcp-client internal/sys-mcp-proxy
git commit -m "fix(gitignore): scope binary exclusions to repo root; add center/proxy/client source"
```

### 3. 从 git 历史中清除二进制

使用 `git-filter-repo` 重写所有提交，彻底删除二进制文件：

```bash
git-filter-repo --path sys-mcp-agent --invert-paths --force
```

执行后 `git-filter-repo` 会自动移除 `origin` remote，需要手动恢复：

```bash
git remote add origin git@github.com:jimyag/sys-mcp.git
```

### 4. 强制推送

```bash
git push --force origin main
```

## 验证

```bash
# 确认历史中不再有任何二进制文件
git log --all --diff-filter=A --name-only --format="" | grep "^sys-mcp"
# 输出为空则表示清理成功

# 确认源码目录不再被忽略
git check-ignore -v internal/sys-mcp-center/config/config.go
# 无输出则表示该文件不被忽略
```

## 预防措施

- 在 `.gitignore` 中，根级别二进制一律使用 `/` 前缀。
- 推荐将构建产物统一输出到 `bin/` 目录（已在 `Taskfile.yaml` 中配置），这样只需忽略 `bin/` 一个目录，不易出错。
- 建议在 CI 中添加检查：若检测到二进制文件被暂存，构建失败并给出提示。
