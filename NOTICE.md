# Source reference and adoption record

This repository contains a minimal Go runtime, an optional HTTP provider and
fictional embedded examples. It does not vendor the complete EasyAgent SDK.

The message, tool-call and Chat Completions streaming protocol shapes are
adapted from the public EasyAgent project:

- Upstream: https://github.com/hwj123hwj/easyagent
- Reference commit: `d90804fbe5cf488e6ace810cff925f7cc8b8c6d9`
- Upstream license: MIT, copyright (c) 2026 hwj123hwj
- This project retains that copyright and MIT license in LICENSE and source headers.

## File mapping and changes

| Upstream reference | Local adaptation | Changes |
| --- | --- | --- |
| sdk/ai/types.go | core/model/model.go | Text-only messages and tool calls; omit workspace, images, private reasoning, model registry and pricing. Host supplies model policy. |
| sdk/ai/stream.go, sdk/ai/providers/interface.go | core/model/model.go, core/event/event.go | Replace mutable channel/result streams with a synchronous Provider callback; public events contain only text/progress and approved metadata. |
| sdk/ai/providers/openai.go | providers/openai/openai.go | Adapt wire message/chunk and indexed tool-fragment assembly; exact configured endpoint, bounded SSE parser, required terminal markers, no retry or raw error body, no private reasoning output. |
| sdk/ai/providers/httpclient.go | providers/openai/openai.go | Retain the cancellation/HTTP timeout concept; use standard HTTP client deadlines, no copied watchdog/retry subsystem. |

These are selective protocol adaptations and rewritten implementations, not
unchanged copies of whole upstream files. tool/schema, runner, approval binding,
budget enforcement and fictional host examples are implemented for this project.
No upstream AgentSession, Goal, Workflow, JSONL history, command/file tools,
terminal UI or dynamic plugin runtime is included.

Updates require review of the exact upstream diff, this mapping and the runtime
contract tests. Source embedding into another host must preserve LICENSE/NOTICE,
record the adopted project commit, import changes and host modifications.

Host integrations and private business adapters are outside this repository.
Examples use fictional identities/records; their approval and deduplication
callbacks are demonstrations, not production persistence implementations.
