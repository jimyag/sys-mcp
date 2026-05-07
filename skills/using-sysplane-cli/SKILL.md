---
name: using-sysplane-cli
description: Use when operating the sysplane CLI from a terminal, especially when reading files or invoking builtin actions and command templates on remote nodes and needing the single unified commands invoke workflow
---

# Using sysplane CLI

## Overview

`sysplane` has one execution entry: `commands invoke`.
Do not switch to `fs ...`, `sys ...`, or `templates invoke`; those are not the canonical workflow.

## When to Use

- Need to run a builtin action like `fs.read`, `fs.list`, `fs.stat`, `fs.write`, `sys.info`, `sys.hardware`
- Need to invoke a command template like `echo.hello`
- Need a copy-pasteable CLI command for a teammate or script

Do not use this skill for WebUI usage or direct HTTP API examples.

## Quick Reference

- Global connection: `--server ... --token ...` or `SYSPLANE_SERVER` + `SYSPLANE_TOKEN`
- Single target: use `--node <node-id>`
- Multiple targets: use `--nodes n1,n2`
- Parameters: always use `--params '<json>'` or `--params-file <file>`
- Execution entry: always `sysplane commands invoke <action-or-template> ...`

## Canonical Patterns

Single node builtin:

```bash
sysplane --server http://127.0.0.1:18880 --token "$TOKEN" \
  commands invoke fs.read --node n1 --params '{"path":"/etc/hostname"}'
```

Single node template:

```bash
sysplane --server http://127.0.0.1:18880 --token "$TOKEN" \
  commands invoke echo.hello --node n1 --params '{}'
```

With environment variables:

```bash
export SYSPLANE_SERVER=http://127.0.0.1:18880
export SYSPLANE_TOKEN="$TOKEN"
sysplane commands invoke sys.info --node n1 --params '{}'
```

## Common Mistakes

| Mistake | Use instead |
| --- | --- |
| `sysplane fs read ...` | `sysplane commands invoke fs.read ...` |
| `sysplane templates invoke ...` | `sysplane commands invoke <template> ...` |
| Single node with `--nodes n1` | Single node with `--node n1` |
| Ad-hoc flags for builtin params | `--params '<json>'` |
