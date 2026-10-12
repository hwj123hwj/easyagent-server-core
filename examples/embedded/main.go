// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// All data, identities, approvals and operations in this example are fictional.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
	"github.com/hwj123hwj/easyagent-server-core/providers/openai"
)

type store struct {
	mu         sync.Mutex
	retries    int
	operations map[string]tool.Result
}
type report struct {
	Outcome     runner.Outcome
	SideEffects int
}

func main() {
	scenario := flag.String("scenario", "query", "query, history, diagnostics, pending, approved, denied, unknown, partial, duplicate")
	useHTTP := flag.Bool("http", false, "explicitly use EA_MODEL_ENDPOINT / EA_MODEL_KEY / EA_MODEL instead of the offline model")
	flag.Parse()
	var provider model.Provider
	if *useHTTP {
		p, err := openai.New(openai.Config{Endpoint: os.Getenv("EA_MODEL_ENDPOINT"), APIKey: os.Getenv("EA_MODEL_KEY")})
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid HTTP provider configuration")
			os.Exit(1)
		}
		provider = p
	}
	enc := json.NewEncoder(os.Stdout)
	r, err := runScenario(context.Background(), *scenario, provider, func(e event.Event) error { return enc.Encode(e) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("state=%s model_calls=%d tool_calls=%d side_effects=%d\n", r.Outcome.State, r.Outcome.ModelCalls, r.Outcome.ToolCalls, r.SideEffects)
}

func runScenario(ctx context.Context, scenario string, provider model.Provider, events event.Handler) (report, error) {
	switch scenario {
	case "query", "history", "diagnostics", "pending", "approved", "denied", "unknown", "partial", "duplicate":
	default:
		return report{}, errors.New("unknown scenario")
	}
	s := &store{operations: map[string]tool.Result{}}
	write := scenario != "query" && scenario != "history" && scenario != "diagnostics"
	if provider == nil {
		provider = scripted(scenario)
	}
	modelName := "fictional-model"
	if os.Getenv("EA_MODEL") != "" {
		modelName = os.Getenv("EA_MODEL")
	}
	idSchema := tool.Schema{Type: "object", Properties: map[string]tool.Schema{"job_id": {Type: "string", Enum: []string{"job-demo-1"}}}, Required: []string{"job_id"}}
	base := func(name string, input tool.Schema, execute func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error)) tool.Tool {
		return tool.Tool{Spec: tool.Spec{Name: name, Version: "1", Description: name + " for fictional records", Effect: tool.Read, Input: input, Timeout: time.Second, MaxResultBytes: 4096}, Execute: execute}
	}
	cost := base("cost.summary", tool.Schema{Type: "object", Properties: map[string]tool.Schema{"days": {Type: "integer"}}, Required: []string{"days"}},
		func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, progress tool.Progress) (tool.Result, error) {
			var args struct {
				Days int `json:"days"`
			}
			if json.Unmarshal(c.Arguments, &args) != nil || args.Days != 7 {
				return tool.Result{Status: tool.Invalid, Summary: "Only the fictional seven-day window is supported."}, nil
			}
			if err := progress("正在查询虚构的近 7 天成本"); err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Status: tool.Available, EvidenceID: "cost-demo-7d", Summary: "严格7天，虚构总成本 USD 487.50。"}, nil
		})
	runs := base("job.runs", idSchema, func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
		if err := p("正在核对虚构任务运行记录"); err != nil {
			return tool.Result{}, err
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.retries > 0 {
			return tool.Result{Status: tool.Available, EvidenceID: "retry-status-demo-1", Summary: "已核对虚构操作记录：重试请求已登记，任务结果仍待观察。"}, nil
		}
		return tool.Result{Status: tool.Available, EvidenceID: "run-demo-1", Summary: "虚构任务失败，产物未发布；这里只读查询。"}, nil
	})
	artifacts := base("artifacts.inspect", idSchema, func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
		if err := p("正在检查虚构完成标记"); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Status: tool.Missing, EvidenceID: "artifact-demo-1", Summary: "虚构完成标记缺失；建议人工核对失败原因。"}, nil
	})
	definition := runner.Definition{ID: "data-analyst", Version: "1", Model: modelName, System: "只使用已注册工具和当前授权数据；证据引用格式为 [[evidence:id]]。", Tools: []tool.Tool{cost}}
	if scenario != "query" && scenario != "history" {
		definition.ID = "artifact-diagnostics"
		definition.Tools = []tool.Tool{runs, artifacts}
	}
	deps := runner.Dependencies{Provider: provider, Authorizer: tool.AuthorizerFunc(func(ctx context.Context, ec tool.ExecutionContext, c tool.Call) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if ec.Principal.TenantID != "demo" || ec.Principal.SubjectID != "demo-user" {
			return errors.New("out of scope")
		}
		return nil
	})}
	if write {
		retry := base("job.retry", idSchema, func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			// Example-only host deduplication. Production must persist this with the
			// business operation and bind operation ID to principal/tool/arguments.
			if previous, ok := s.operations[ec.OperationID]; ok {
				return previous, nil
			}
			s.retries++
			result := tool.Result{Status: tool.Applied, EvidenceID: "retry-demo-1", Summary: "虚构重试已登记；不代表任务已运行成功。"}
			if scenario == "partial" {
				result.Status = tool.Partial
				result.Summary = "虚构请求已登记，但后续状态更新未完成。"
			}
			s.operations[ec.OperationID] = result
			if scenario == "unknown" {
				return tool.Result{}, errors.New("fictional connection lost after application")
			}
			return result, nil
		})
		retry.Spec.Effect = tool.Write
		retry.Spec.RequiresApproval = true
		retry.Prepare = func(ctx context.Context, ec tool.ExecutionContext, c tool.Call) (tool.Intent, error) {
			return tool.Intent{Summary: "为虚构任务 job-demo-1 登记一次重试请求", Resources: []tool.Resource{{ID: "job-demo-1", Version: "definition-v1"}}}, nil
		}
		definition.Tools = append(definition.Tools, retry)
		budget := runner.DefaultBudget(runner.Agent)
		budget.MaxWrites = 1
		definition.AgentBudget = budget
		deps.OperationID = func(context.Context, tool.ExecutionContext, tool.Call) (string, error) { return "demo-intent-1", nil }
		if scenario != "pending" {
			// This synthetic gate is not a production approval endpoint. A real host
			// must authenticate, bind, expire and consume a human approval record.
			deps.Approval = tool.ApprovalFunc(func(ctx context.Context, p tool.Proposal) (tool.Approval, error) {
				decision := tool.Approved
				if scenario == "denied" {
					decision = tool.Rejected
				}
				return tool.Approval{Decision: decision, Fingerprint: p.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}, nil
			})
		}
	}
	r, err := runner.New(definition, deps)
	if err != nil {
		return report{}, err
	}
	req := runner.Request{Principal: tool.Principal{TenantID: "demo", SubjectID: "demo-user"}, RunID: "demo-run-1", Mode: runner.Chat, Question: "近7天花了多少钱？"}
	if definition.ID == "artifact-diagnostics" {
		req.Mode = runner.Agent
		req.Question = "检查虚构任务和产物，给出证据与下一步。"
	}
	if write {
		req.Question = "诊断后，申请为虚构任务登记一次重试。"
	}
	if scenario == "history" {
		req.History = []model.Message{{Role: model.User, Text: "近7天成本？"}, {Role: model.Assistant, Text: "虚构成本 USD 487.50，严格7天。"}}
		req.Question = "继续解释刚才的统计口径。"
	}
	out, err := r.Run(ctx, req, events)
	if scenario == "duplicate" && err == nil {
		req.RunID = "demo-run-2"
		out, err = r.Run(ctx, req, events)
	}
	if scenario == "denied" && errors.Is(err, runner.ErrApproval) {
		err = nil
	}
	if (scenario == "unknown" || scenario == "partial") && errors.Is(err, runner.ErrUncertain) {
		err = nil
	}
	return report{Outcome: out, SideEffects: s.retries}, err
}

func scripted(scenario string) model.Provider {
	return model.ProviderFunc(func(ctx context.Context, req model.Request, emit func(string) error) (model.Response, error) {
		if ctx.Err() != nil {
			return model.Response{}, ctx.Err()
		}
		toolResults := 0
		for _, m := range req.Messages {
			if m.Role == model.Tool {
				toolResults++
			}
		}
		call := func(name string, args string) model.Response {
			return model.Response{FinishReason: model.ToolUse, ToolCalls: []model.ToolCall{{ID: fmt.Sprintf("call-%d", toolResults+1), Name: name, Arguments: json.RawMessage(args)}}}
		}
		var answer string
		switch scenario {
		case "history":
			answer = "延续上一轮：按严格7天统计，虚构数据为 USD 487.50。"
		case "query":
			if toolResults == 0 {
				return call("cost.summary", `{"days":7}`), nil
			}
			answer = "近7天虚构总成本为 USD 487.50。[[evidence:cost-demo-7d]]"
		default:
			if toolResults == 0 {
				return call("job.runs", `{"job_id":"job-demo-1"}`), nil
			}
			if toolResults == 1 {
				if scenario == "diagnostics" {
					return call("artifacts.inspect", `{"job_id":"job-demo-1"}`), nil
				}
				return call("job.retry", `{"job_id":"job-demo-1"}`), nil
			}
			if scenario == "diagnostics" {
				answer = "虚构任务失败且完成标记缺失，建议人工排查；尚未修复。[[evidence:run-demo-1]] [[evidence:artifact-demo-1]]"
			} else {
				if toolResults == 2 {
					return call("job.runs", `{"job_id":"job-demo-1"}`), nil
				}
				answer = "已核对虚构操作记录：重试请求已登记，任务结果仍需观察。[[evidence:retry-demo-1]] [[evidence:retry-status-demo-1]]"
			}
		}
		// Split on rune boundaries, as required by the Provider contract.
		runes := []rune(answer)
		for _, part := range []string{string(runes[:len(runes)/2]), string(runes[len(runes)/2:])} {
			if err := emit(part); err != nil {
				return model.Response{}, err
			}
		}
		return model.Response{FinishReason: model.Stop, Text: answer}, nil
	})
}
