// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func TestToolFeedbackContinuesThroughMultipleCorrections(t *testing.T) {
	calls, turns := 0, 0
	tt := read(func(_ context.Context, _ tool.ExecutionContext, c tool.Call, _ tool.Progress) (tool.Result, error) {
		calls++
		if strings.Contains(string(c.Arguments), "broken") {
			return tool.Result{}, errors.New("private database credentials")
		}
		return tool.Result{Status: tool.Available, EvidenceID: "actual", Summary: "fictional result"}, nil
	})
	d := definition(tt)
	d.Discovery = &runner.Discovery{Groups: []runner.ToolGroup{{ID: "business", Description: "fictional queries", Tools: []string{"job.runs"}}}, MaxActiveTools: 2}
	responses := []model.Response{
		proposed("invented_ranking", "c1", `{}`),
		proposed("job.runs", "c2", `{"id":"good"}`), // registered but still hidden
		proposed(runner.DiscoverTools, "c3", `{"groups":["business"]}`),
		proposed("job.runs", "c4", `{"bad":"field"}`),
		proposed("job.runs", "c5", `{"id":"broken"}`),
		proposed("job.runs", "c6", `{"id":"good"}`),
		{FinishReason: model.Stop, Text: "Found [[evidence:actual]]"},
	}
	expected := []string{"tool_unavailable", "tool_unavailable", "", "invalid_arguments", "tool_execution_failed", ""}
	provider := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		if turns > 0 {
			var result tool.Result
			last := r.Messages[len(r.Messages)-1]
			if last.Role != model.Tool || json.Unmarshal([]byte(last.Text), &result) != nil || result.Code != expected[turns-1] {
				t.Fatalf("missing feedback at %d: %+v", turns, last)
			}
		}
		if turns >= len(responses) {
			t.Fatal("extra model call")
		}
		v := responses[turns]
		turns++
		return v, nil
	})
	var events []event.Event
	out, err := build(t, d, deps(provider)).Run(t.Context(), req(), func(e event.Event) error { events = append(events, e); return nil })
	if err != nil || out.State != runner.Completed || out.ToolCalls != 6 || out.ModelCalls != 7 || calls != 2 || len(out.Observations) != 6 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
	}
	raw, _ := json.Marshal(struct {
		History []model.Message
		Events  []event.Event
	}{out.History, events})
	if strings.Contains(string(raw), "credentials") {
		t.Fatal("raw host error leaked")
	}
}

func TestInvalidSelectionAndArgumentsConsumeExistingBudgets(t *testing.T) {
	for _, kind := range []string{"selection repeat", "arguments repeat", "tool budget", "model budget"} {
		t.Run(kind, func(t *testing.T) {
			executions := 0
			tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				executions++
				return tool.Result{Status: tool.Available}, nil
			})
			d := definition(tt)
			d.AgentBudget.MaxWrites = 0
			if kind == "tool budget" {
				d.AgentBudget.MaxToolCalls = 2
			}
			if kind == "model budget" {
				d.AgentBudget.MaxModelCalls = 2
			}
			turn := 0
			provider := model.ProviderFunc(func(context.Context, model.Request, func(string) error) (model.Response, error) {
				turn++
				name, args := "invented", `{}`
				if kind == "arguments repeat" {
					name, args = "job.runs", `{"no":"field"}`
				}
				if kind == "tool budget" || kind == "model budget" {
					name = fmt.Sprintf("invented%d", turn)
				}
				return proposed(name, fmt.Sprintf("call%d", turn), args), nil
			})
			out, err := build(t, d, deps(provider)).Run(t.Context(), req(), nil)
			want := runner.ErrLoop
			if kind == "tool budget" || kind == "model budget" {
				want = runner.ErrBudget
			}
			if !errors.Is(err, want) || executions != 0 || out.State != runner.Interrupted {
				t.Fatalf("out=%+v err=%v executed=%d", out, err, executions)
			}
		})
	}
}

func TestMalformedCallRemainsProtocolFailureBeforeAnyExecution(t *testing.T) {
	for _, kind := range []string{"unsafe name", "duplicate id", "invalid json"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{Status: tool.Available}, nil
			})
			response := proposed("job.runs", "c1", `{"id":"good"}`)
			switch kind {
			case "unsafe name":
				response.ToolCalls = append(response.ToolCalls, model.ToolCall{ID: "c2", Name: "unsafe name", Arguments: json.RawMessage(`{}`)})
			case "duplicate id":
				response.ToolCalls = append(response.ToolCalls, response.ToolCalls[0])
			case "invalid json":
				response.ToolCalls[0].Arguments = json.RawMessage(`{`)
			}
			_, err := build(t, definition(tt), deps(script(response))).Run(t.Context(), req(), nil)
			if err == nil || calls != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestBadReadResultsAreSafeFeedbackWithoutFalseEvidence(t *testing.T) {
	for _, kind := range []string{"invalid status", "oversized", "error with evidence"} {
		t.Run(kind, func(t *testing.T) {
			tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				switch kind {
				case "invalid status":
					return tool.Result{Status: tool.Applied, EvidenceID: "forged"}, nil
				case "oversized":
					return tool.Result{Status: tool.Available, EvidenceID: "forged", Summary: strings.Repeat("x", 2000)}, nil
				default:
					return tool.Result{Status: tool.Available, EvidenceID: "forged", Summary: "private password"}, errors.New("private cause")
				}
			})
			provider := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
				if len(r.Messages) == 1 {
					return proposed("job.runs", "read", `{"id":"job-1"}`), nil
				}
				var result tool.Result
				if json.Unmarshal([]byte(r.Messages[len(r.Messages)-1].Text), &result) != nil || result.Status != tool.Unavailable || result.EvidenceID != "" || strings.Contains(result.Summary, "private") {
					t.Fatalf("unsafe result=%+v", result)
				}
				return model.Response{FinishReason: model.Stop, Text: "Evidence unavailable."}, nil
			})
			out, err := build(t, definition(tt), deps(provider)).Run(t.Context(), req(), nil)
			if err != nil || out.State != runner.Completed {
				t.Fatal(err)
			}
		})
	}
}
