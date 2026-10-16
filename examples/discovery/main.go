// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// This offline host demonstrates model-directed, bounded capability discovery.
// All identities and results are fictional; no network or database is used.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func main() {
	if err := run(); err != nil {
		panic(err)
	}
}
func run() error {
	read := func(name, summary string) tool.Tool {
		return tool.Tool{Spec: tool.Spec{Name: name, Version: "1", Description: summary, Effect: tool.Read, Input: tool.Schema{Type: "object"}, Timeout: time.Second, MaxResultBytes: 1024}, Execute: func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
			return tool.Result{Status: tool.Available, Summary: summary}, nil
		}}
	}
	turn := 0
	provider := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		turn++
		names := []string{}
		for _, t := range r.Tools {
			names = append(names, t.Name)
		}
		fmt.Printf("turn %d schemas: %v\n", turn, names)
		name, args := "", `{}`
		switch turn {
		case 1:
			name, args = runner.DiscoverTools, `{"groups":["business.usage"]}`
		case 2:
			name = "usage.summary"
		case 3:
			name, args = runner.DiscoverTools, `{"groups":["system.health"]}`
		case 4:
			name = "queue.health"
		default:
			return model.Response{FinishReason: model.Stop, Text: "Fictional usage and queue evidence checked in one conversation."}, nil
		}
		return model.Response{FinishReason: model.ToolUse, ToolCalls: []model.ToolCall{{ID: fmt.Sprint(turn), Name: name, Arguments: json.RawMessage(args)}}}, nil
	})
	// A real host builds BOTH the registry and directory from authenticated
	// capabilities, then rechecks current authority/resources in Authorizer.
	r, err := runner.New(runner.Definition{ID: "fictional-host", Version: "1", Model: "offline", Tools: []tool.Tool{read("usage.summary", "Fictional usage: 10 requests"), read("queue.health", "Fictional queue: healthy")}, Discovery: &runner.Discovery{Groups: []runner.ToolGroup{{ID: "business.usage", Description: "Business usage analysis", Tools: []string{"usage.summary"}}, {ID: "system.health", Description: "System queue health", Tools: []string{"queue.health"}}}, MaxActiveTools: 2}}, runner.Dependencies{Provider: provider, Authorizer: tool.AuthorizerFunc(func(_ context.Context, ec tool.ExecutionContext, _ tool.Call) error {
		if ec.Principal != (tool.Principal{TenantID: "fictional", SubjectID: "demo"}) {
			return runner.ErrDenied
		}
		return nil
	})})
	if err != nil {
		return err
	}
	out, err := r.Run(context.Background(), runner.Request{Principal: tool.Principal{TenantID: "fictional", SubjectID: "demo"}, RunID: "demo", Mode: runner.Agent, Question: "Inspect usage, then check queue health."}, nil)
	if err != nil {
		return err
	}
	if out.State != runner.Completed || out.ToolCalls != 4 {
		return fmt.Errorf("unexpected outcome: %s", out.State)
	}
	fmt.Println(out.Text)
	return nil
}
