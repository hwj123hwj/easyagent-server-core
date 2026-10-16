# AGENTS.md

面向编码代理与本仓库协作者的工作约定。项目介绍、核心边界与接入说明见 [README.md](README.md) 与 [docs/](docs/)。

## 分支与合并

- 长期只保留 `main`。当前任务可开短期 `feat/`、`fix/`、`docs/` 分支；一个任务一个分支，不积累已完成的分支。
- 提交标题描述实际改变，沿用 `feat:` / `fix:` / `docs:` 前缀。
- 改动经 PR 合并进 `main`；CI（gofmt、go vet、go test -race、示例场景）通过后合并。
- PR 合并后必须收尾，不能留给下一次：切回并同步 `main`，确认提交已进入 `main`，删除对应的本地与远端任务分支，再 `git fetch --prune origin`。
- 删除前逐项核实：没有主线之外的独有提交、没有未提交或未跟踪改动、没有会话或进程仍在使用。需要保留的先整合或做可恢复备份；不用强制删除制造“只剩 main”。
- 汇报完成前检查本地与远端分支；仍有未合并或使用中的分支时说明原因。

## 验证

从仓库根目录执行，与 CI 对齐：

```bash
test -z "$(gofmt -l core providers examples integration)"
go vet ./...
go test -race ./...
go run ./examples/embedded -scenario diagnostics
go run ./examples/embedded -scenario duplicate
```

示例为虚构数据、不访问网络；不调用真实模型或生产服务。只改文档时可仅核对链接与示例名称，合并仍以 CI 为准。

## 约定

- 零第三方 Go module 依赖是当前设计约束；引入依赖须先在 PR 说明理由。
- 核心能力边界（不提供 shell、任意脚本或代码执行、文件编辑、原始 SQL、任意 URL 请求；逐次授权、鉴权与审批分离）见 [README](README.md) 与 [威胁模型](docs/THREAT-MODEL.md)，改动不得绕过。
- 涉及协议或集成的改动同步更新 `docs/INTEGRATION.md` 等对应文档。
