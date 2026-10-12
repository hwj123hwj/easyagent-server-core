// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func req() runner.Request {
	return runner.Request{Principal: tool.Principal{TenantID: "test", SubjectID: "user"}, RunID: "run-1", Question: "inspect", Mode: runner.Agent}
}
func definition(tools ...tool.Tool) runner.Definition {
	b := runner.DefaultBudget(runner.Agent)
	b.MaxWrites = 1
	return runner.Definition{ID: "assistant", Version: "1", Model: "test-model", Tools: tools, AgentBudget: b}
}
func read(execute func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error)) tool.Tool {
	return tool.Tool{Spec: tool.Spec{Name: "job.runs", Version: "1", Effect: tool.Read, Input: tool.Schema{Type: "object", Properties: map[string]tool.Schema{"id": {Type: "string", MinLength: 1, MaxLength: 32}}, Required: []string{"id"}}, Timeout: time.Second, MaxResultBytes: 1024}, Execute: execute}
}
func write(execute func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error)) tool.Tool {
	t := read(execute)
	t.Spec.Name = "job.retry"
	t.Spec.Effect = tool.Write
	t.Spec.RequiresApproval = true
	t.Prepare = func(context.Context, tool.ExecutionContext, tool.Call) (tool.Intent, error) {
		return tool.Intent{Summary: "retry one fictional job", Resources: []tool.Resource{{ID: "job-1", Version: "v1"}}}, nil
	}
	return t
}
func proposed(name, id, args string) model.Response {
	return model.Response{FinishReason: model.ToolUse, ToolCalls: []model.ToolCall{{ID: id, Name: name, Arguments: json.RawMessage(args)}}}
}
func script(responses ...model.Response) model.Provider {
	i := 0
	return model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
		if i >= len(responses) {
			return model.Response{}, errors.New("unexpected model request")
		}
		response := responses[i]
		i++
		if response.Text != "" {
			if err := emit(response.Text); err != nil {
				return model.Response{}, err
			}
		}
		return response, nil
	})
}
func deps(provider model.Provider) runner.Dependencies {
	return runner.Dependencies{Provider: provider, Authorizer: tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error { return nil }), OperationID: func(context.Context, tool.ExecutionContext, tool.Call) (string, error) { return "intent-1", nil }}
}
func approve() tool.ApprovalGate {
	return tool.ApprovalFunc(func(ctx context.Context, p tool.Proposal) (tool.Approval, error) {
		return tool.Approval{Decision: tool.Approved, Fingerprint: p.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}, nil
	})
}
func build(t *testing.T, d runner.Definition, dependencies runner.Dependencies) *runner.Runner {
	t.Helper()
	r, err := runner.New(d, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func done(events []event.Event) bool {
	for _, e := range events {
		if e.Kind == event.Done {
			return true
		}
	}
	return false
}

func TestRegisteredAuthorizedQueryAndEvidence(t *testing.T) {
	checks, calls := 0, 0
	tt := read(func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
		calls++
		if string(c.Arguments) != `{"id":"job-1"}` || c.Version != "1" || ec.AssistantID != "assistant" {
			t.Fatal("untrusted or missing context")
		}
		if err := p("checking job"); err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Status: tool.Available, EvidenceID: "ev-1", Summary: "failed"}, nil
	})
	d := deps(script(proposed("job.runs", "call-1", `{ "id": "job-1" }`), model.Response{FinishReason: model.Stop, Text: "failed [[evidence:ev-1]]"}))
	d.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error { checks++; return nil })
	d.Approval = tool.ApprovalFunc(func(context.Context, tool.Proposal) (tool.Approval, error) {
		t.Fatal("read requested approval")
		return tool.Approval{}, nil
	})
	var events []event.Event
	out, err := build(t, definition(tt), d).Run(context.Background(), req(), func(e event.Event) error { events = append(events, e); return nil })
	if err != nil || out.State != runner.Completed || calls != 1 || checks != 1 || !done(events) {
		t.Fatalf("out=%+v err=%v checks=%d calls=%d", out, err, checks, calls)
	}
	if len(out.History) != 4 || out.History[2].Role != model.Tool {
		t.Fatal("missing tool conversation")
	}
}

func TestRejectBeforeSideEffect(t *testing.T) {
	for _, tc := range []struct {
		name, args, tool string
		deny             bool
		want             error
	}{
		{"unknown tool", `{"id":"job-1"}`, "shell", false, runner.ErrTool},
		{"extra parameter", `{"id":"job-1","command":"print"}`, "job.runs", false, tool.ErrArguments},
		{"duplicate parameter", `{"id":"job-1","id":"job-2"}`, "job.runs", false, tool.ErrArguments},
		{"denied", `{"id":"job-1"}`, "job.runs", true, runner.ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{Status: tool.Available}, nil
			})
			d := deps(script(proposed(tc.tool, "call-1", tc.args)))
			if tc.deny {
				d.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error {
					return errors.New("secret policy detail")
				})
			}
			var events []event.Event
			_, err := build(t, definition(tt), d).Run(context.Background(), req(), func(e event.Event) error { events = append(events, e); return nil })
			if !errors.Is(err, tc.want) || calls != 0 || done(events) {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			b, _ := json.Marshal(events)
			if strings.Contains(string(b), "secret") || strings.Contains(err.Error(), "secret") {
				t.Fatal("private cause leaked")
			}
		})
	}
}

func TestMissingAuthorizationOrWriteIdentityFailsConfiguration(t *testing.T) {
	tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		return tool.Result{}, nil
	})
	d := deps(script())
	d.Authorizer = nil
	if _, err := runner.New(definition(tt), d); !errors.Is(err, runner.ErrConfiguration) {
		t.Fatal(err)
	}
	tt.Spec.Effect = tool.Write
	d = deps(script())
	d.OperationID = nil
	if _, err := runner.New(definition(tt), d); !errors.Is(err, runner.ErrConfiguration) {
		t.Fatal(err)
	}
	tt.Spec.Effect = ""
	if _, err := runner.New(definition(tt), deps(script())); err == nil {
		t.Fatal("undeclared effect accepted")
	}
}

func TestApprovalBindingAndRechecks(t *testing.T) {
	for _, name := range []string{"missing", "pending", "rejected", "expired", "fingerprint", "changed target", "revoked", "approved"} {
		t.Run(name, func(t *testing.T) {
			calls, checks := 0, 0
			version := "v1"
			tt := write(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{Status: tool.Applied, Summary: "requested"}, nil
			})
			tt.Prepare = func(context.Context, tool.ExecutionContext, tool.Call) (tool.Intent, error) {
				return tool.Intent{Summary: "retry", Resources: []tool.Resource{{ID: "job-1", Version: version}}}, nil
			}
			d := deps(script(proposed("job.retry", "call-1", `{"id":"job-1"}`), model.Response{FinishReason: model.Stop, Text: "requested"}))
			d.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error {
				checks++
				if name == "revoked" && checks > 1 {
					return errors.New("revoked")
				}
				return nil
			})
			if name != "missing" {
				d.Approval = tool.ApprovalFunc(func(ctx context.Context, p tool.Proposal) (tool.Approval, error) {
					a := tool.Approval{Decision: tool.Approved, Fingerprint: p.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}
					switch name {
					case "pending":
						a.Decision = tool.Pending
					case "rejected":
						a.Decision = tool.Rejected
					case "expired":
						a.ExpiresAt = time.Now().Add(-time.Second)
					case "fingerprint":
						a.Fingerprint = "different"
					case "changed target":
						version = "v2"
					}
					return a, nil
				})
			}
			var events []event.Event
			out, err := build(t, definition(tt), d).Run(context.Background(), req(), func(e event.Event) error { events = append(events, e); return nil })
			switch name {
			case "approved":
				if err != nil || calls != 1 || checks != 2 || !done(events) {
					t.Fatalf("err=%v calls=%d checks=%d", err, calls, checks)
				}
			case "missing", "pending":
				if err != nil || calls != 0 || out.State != runner.PendingApproval || out.Proposal == nil || done(events) {
					t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
				}
			default:
				if err == nil || calls != 0 || done(events) {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
			}
		})
	}
}

func TestApprovalCannotMoveToAnotherAccountOrArguments(t *testing.T) {
	var receipt tool.Approval
	for i := 0; i < 3; i++ {
		id := "job-1"
		request := req()
		if i == 1 {
			request.Principal.SubjectID = "other"
		}
		if i == 2 {
			id = "job-2"
		}
		calls := 0
		tt := write(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
			calls++
			return tool.Result{Status: tool.Applied}, nil
		})
		d := deps(script(proposed("job.retry", "call-1", fmt.Sprintf(`{"id":%q}`, id)), model.Response{FinishReason: model.Stop, Text: "requested"}))
		d.Approval = tool.ApprovalFunc(func(ctx context.Context, p tool.Proposal) (tool.Approval, error) {
			if i == 0 {
				receipt = tool.Approval{Decision: tool.Approved, Fingerprint: p.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}
			}
			return receipt, nil
		})
		_, err := build(t, definition(tt), d).Run(context.Background(), request, nil)
		if i == 0 && (err != nil || calls != 1) {
			t.Fatal(err)
		}
		if i > 0 && (err == nil || calls != 0) {
			t.Fatal("approval reused across scope")
		}
	}
}

func TestWriteFailureIsUnknownAndNeverReplayed(t *testing.T) {
	for _, status := range []tool.Status{tool.Unknown, tool.Partial, tool.Applied} {
		t.Run(string(status), func(t *testing.T) {
			calls := 0
			tt := write(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				if status == tool.Applied {
					return tool.Result{}, errors.New("connection lost after side effect")
				}
				return tool.Result{Status: status, Summary: "unconfirmed"}, nil
			})
			d := deps(script(proposed("job.retry", "call-1", `{"id":"job-1"}`)))
			d.Approval = approve()
			var events []event.Event
			out, err := build(t, definition(tt), d).Run(context.Background(), req(), func(e event.Event) error { events = append(events, e); return nil })
			if !errors.Is(err, runner.ErrUncertain) || calls != 1 || out.ModelCalls != 1 || done(events) || len(out.Observations) != 1 {
				t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
			}
			if status == tool.Applied && out.Observations[0].Result.Status != tool.Unknown {
				t.Fatal("lost ambiguous side effect")
			}
		})
	}
}

func TestBudgetsRepetitionAndCancellation(t *testing.T) {
	for _, name := range []string{"model", "tool", "repeat", "write disabled", "result reserve", "cancel"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{Status: tool.Available, Summary: "ok"}, nil
			})
			modelCalls := 0
			provider := model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
				modelCalls++
				return proposed(tt.Spec.Name, fmt.Sprintf("call-%d", modelCalls), `{"id":"job-1"}`), nil
			})
			def := definition(tt)
			d := deps(provider)
			expected := runner.ErrBudget
			expectedCalls := 1
			switch name {
			case "model":
				def.AgentBudget.MaxModelCalls = 1
			case "tool":
				def.AgentBudget.MaxToolCalls = 1
			case "repeat":
				def.AgentBudget.MaxRepeatedReads = 1
				expected = runner.ErrLoop
			case "write disabled":
				tt.Spec.Effect = tool.Write
				def.Tools = []tool.Tool{tt}
				def.AgentBudget.MaxWrites = 0
				expectedCalls = 0
			case "result reserve":
				tt.Spec.MaxResultBytes = def.AgentBudget.MaxOutputBytes
				def.Tools = []tool.Tool{tt}
				expectedCalls = 0
			case "cancel":
				expected = context.Canceled
				expectedCalls = 0
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if name == "cancel" {
				cancel()
			}
			out, err := build(t, def, d).Run(ctx, req(), nil)
			if !errors.Is(err, expected) || calls != expectedCalls || out.State == runner.Completed {
				t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
			}
		})
	}
}

func TestStreamFailureHandlerFailureAndUnknownEvidence(t *testing.T) {
	for _, name := range []string{"断流", "handler", "mismatched preview", "unknown evidence"} {
		t.Run(name, func(t *testing.T) {
			provider := model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
				text := "partial"
				if name == "unknown evidence" {
					text = "claim [[evidence:fake]]"
				}
				_ = emit(text) // Intentionally hostile provider ignores callback error.
				if name == "断流" {
					return model.Response{}, errors.New("secret upstream body")
				}
				if name == "mismatched preview" {
					text = "different"
				}
				return model.Response{FinishReason: model.Stop, Text: text}, nil
			})
			var events []event.Event
			out, err := build(t, definition(), deps(provider)).Run(context.Background(), req(), func(e event.Event) error {
				events = append(events, e)
				if name == "handler" && e.Kind == event.Delta {
					return errors.New("disconnected")
				}
				return nil
			})
			if err == nil || out.State == runner.Completed || done(events) {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw error leaked")
			}
		})
	}
}

func TestToolDeadlineAndRevokedBetweenCalls(t *testing.T) {
	tt := read(func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
		<-ctx.Done()
		return tool.Result{}, ctx.Err()
	})
	tt.Spec.Timeout = 10 * time.Millisecond
	out, err := build(t, definition(tt), deps(script(proposed("job.runs", "call-1", `{"id":"job-1"}`)))).Run(context.Background(), req(), nil)
	if !errors.Is(err, context.DeadlineExceeded) || len(out.Observations) != 1 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	calls, checks := 0, 0
	tt = read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		calls++
		return tool.Result{Status: tool.Available}, nil
	})
	d := deps(script(model.Response{FinishReason: model.ToolUse, ToolCalls: []model.ToolCall{{ID: "c1", Name: "job.runs", Arguments: json.RawMessage(`{"id":"job-1"}`)}, {ID: "c2", Name: "job.runs", Arguments: json.RawMessage(`{"id":"job-2"}`)}}}))
	d.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error {
		checks++
		if checks == 2 {
			return errors.New("revoked")
		}
		return nil
	})
	_, err = build(t, definition(tt), d).Run(context.Background(), req(), nil)
	if !errors.Is(err, runner.ErrDenied) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestConcurrentHistoriesAndDefinitionSnapshot(t *testing.T) {
	var calls atomic.Int32
	tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		calls.Add(1)
		return tool.Result{Status: tool.Available}, nil
	})
	provider := model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
		if strings.Contains(string(r.Tools[0].Parameters), "injected") {
			t.Error("definition snapshot was mutated")
		}
		text := r.Messages[len(r.Messages)-1].Text
		return model.Response{Text: text, FinishReason: model.Stop}, nil
	})
	def := definition(tt)
	r := build(t, def, deps(provider))
	def.Tools[0].Spec.Input.Properties["injected"] = tool.Schema{Type: "string"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			request := req()
			request.Question = fmt.Sprintf("question-%d", i)
			request.History = []model.Message{{Role: model.Assistant, Text: "previous"}}
			out, err := r.Run(context.Background(), request, nil)
			if err != nil || out.Text != request.Question || len(request.History) != 1 {
				t.Errorf("history mixed: out=%+v err=%v", out, err)
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() != 0 {
		t.Fatal("unexpected tool invocation")
	}
}

func TestInputOutputBudgetsAndFinalValidation(t *testing.T) {
	for _, name := range []string{"input", "delta", "tool result", "final"} {
		t.Run(name, func(t *testing.T) {
			def := definition()
			d := deps(model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
				text := "answer"
				if name == "delta" {
					text = strings.Repeat("a", 1000)
				}
				if err := emit(text); err != nil {
					return model.Response{}, err
				}
				return model.Response{Text: text, FinishReason: model.Stop}, nil
			}))
			want := runner.ErrBudget
			switch name {
			case "input":
				def.AgentBudget.MaxInputBytes = 1
			case "delta":
				def.AgentBudget.MaxOutputBytes = 128
			case "tool result":
				tt := read(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
					return tool.Result{Status: tool.Available, Summary: strings.Repeat("a", 2000)}, nil
				})
				def.Tools = []tool.Tool{tt}
				d.Provider = script(proposed("job.runs", "c1", `{"id":"job-1"}`))
				want = runner.ErrTool
			case "final":
				d.ValidateFinal = func(context.Context, runner.Outcome) error { return runner.ErrEvidence }
				want = runner.ErrEvidence
			}
			var events []event.Event
			out, err := build(t, def, d).Run(context.Background(), req(), func(e event.Event) error { events = append(events, e); return nil })
			if !errors.Is(err, want) || out.State == runner.Completed || done(events) {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		})
	}
}

func TestModePolicyAndExplicitLowRiskWrite(t *testing.T) {
	for _, mode := range []runner.Mode{runner.Chat, runner.Agent} {
		def := definition()
		def.ChatInstructions = "chat policy"
		def.AgentInstructions = "agent policy"
		provider := model.ProviderFunc(func(ctx context.Context, r model.Request, emit func(string) error) (model.Response, error) {
			if !strings.Contains(r.System, string(mode)+" policy") || len(r.Tools) != 0 {
				t.Error("mode changed capabilities or lost policy")
			}
			return model.Response{Text: "answer", FinishReason: model.Stop}, nil
		})
		request := req()
		request.Mode = mode
		if _, err := build(t, def, deps(provider)).Run(context.Background(), request, nil); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	tt := write(func(ctx context.Context, ec tool.ExecutionContext, c tool.Call, p tool.Progress) (tool.Result, error) {
		calls++
		if ec.OperationID != "intent-1" {
			t.Error("write lost stable operation identity")
		}
		return tool.Result{Status: tool.Applied, Summary: "low-risk fixture operation"}, nil
	})
	tt.Spec.RequiresApproval = false
	tt.Prepare = nil
	d := deps(script(proposed("job.retry", "c1", `{"id":"job-1"}`), model.Response{Text: "requested", FinishReason: model.Stop}))
	out, err := build(t, definition(tt), d).Run(context.Background(), req(), nil)
	if err != nil || calls != 1 || out.State != runner.Completed {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
