// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func TestPrivateProviderContinuationIsTransientAndBudgeted(t *testing.T) {
	queries := 0
	calls := 0
	provider := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		calls++
		if calls == 1 {
			out := proposed("job.runs", "call-1", `{"id":"job-1"}`)
			out.ReasoningContent = "provider-private-state"
			return out, nil
		}
		if len(r.Messages) != 3 || r.Messages[1].ReasoningContent != "provider-private-state" {
			t.Fatal("private provider continuation lost")
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "provider-private-state") {
			t.Fatal("private state serialized")
		}
		return model.Response{Text: "done", FinishReason: model.Stop}, nil
	})
	r := build(t, definition(read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		queries++
		return tool.Result{Status: tool.Available, Summary: "fixture"}, nil
	})), deps(provider))
	out, e := r.Run(t.Context(), req(), func(v event.Event) error {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "provider-private-state") {
			t.Fatal("private state emitted")
		}
		return nil
	})
	if e != nil || out.State != runner.Completed || calls != 2 || queries != 1 {
		t.Fatal(out.State, e, calls, queries)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "provider-private-state") {
		t.Fatal("private state persisted")
	}

	called := false
	r = build(t, definition(), deps(model.ProviderFunc(func(context.Context, model.Request, func(string) error) (model.Response, error) {
		called = true
		return model.Response{}, nil
	})))
	q := req()
	q.History = []model.Message{{Role: model.Assistant, Text: "old answer", ReasoningContent: strings.Repeat("x", runner.DefaultBudget(runner.Agent).MaxInputBytes)}}
	_, e = r.Run(t.Context(), q, nil)
	if !errors.Is(e, runner.ErrBudget) || called {
		t.Fatal("private input bypassed budget", e, called)
	}
}
