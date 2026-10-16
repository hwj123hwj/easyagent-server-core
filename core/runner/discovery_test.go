// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func catalogDefinition(execute func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error)) runner.Definition {
	a := read(execute)
	a.Spec.Name = "usage.daily"
	b := read(execute)
	b.Spec.Name = "permissions.read"
	c := write(execute)
	c.Spec.Name = "permissions.update"
	d := definition(a, b, c)
	d.Discovery = &runner.Discovery{Groups: []runner.ToolGroup{{ID: "business.people", Description: "Business: daily person usage", Tools: []string{a.Spec.Name}}, {ID: "system.permissions", Description: "System: read and change member permissions", Tools: []string{b.Spec.Name, c.Spec.Name}}}, MaxActiveTools: 3}
	return d
}
func toolNames(r model.Request) string {
	names := []string{}
	for _, d := range r.Tools {
		names = append(names, d.Name)
	}
	return strings.Join(names, ",")
}
func TestProgressiveDiscoveryCrossDomainAndApproval(t *testing.T) {
	calls := 0
	d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		calls++
		return tool.Result{Status: tool.Available, Summary: "fictional facts"}, nil
	})
	turn := 0
	p := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		turn++
		switch turn {
		case 1:
			if toolNames(r) != runner.DiscoverTools {
				t.Fatalf("eager schemas: %s", toolNames(r))
			}
			return proposed(runner.DiscoverTools, "d1", `{"groups":["business.people"]}`), nil
		case 2:
			if toolNames(r) != "usage.daily,discover_tools" {
				t.Fatal(toolNames(r))
			}
			return proposed("usage.daily", "q1", `{"id":"person-1"}`), nil
		case 3:
			return proposed(runner.DiscoverTools, "d2", `{"groups":["system.permissions"]}`), nil
		case 4:
			if toolNames(r) != "permissions.read,permissions.update,discover_tools" {
				t.Fatal(toolNames(r))
			}
			raw, _ := json.Marshal(r.Messages)
			if !strings.Contains(string(raw), "fictional facts") {
				t.Fatal("lost cross-domain history")
			}
			return proposed("permissions.read", "q2", `{"id":"person-1"}`), nil
		default:
			return proposed("permissions.update", "w1", `{"id":"person-1"}`), nil
		}
	})
	r := build(t, d, deps(p))
	out, e := r.Run(context.Background(), req(), nil)
	if e != nil || out.State != runner.PendingApproval || out.Proposal == nil || calls != 2 || out.ToolCalls != 4 {
		t.Fatalf("state=%s err=%v calls=%d tools=%d", out.State, e, calls, out.ToolCalls)
	}
}
func TestDiscoveryRejectsHiddenCallsAndSameBatchGuess(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "hidden", true: "same batch"}[batch], func(t *testing.T) {
			calls := 0
			d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{}, nil
			})
			response := proposed("usage.daily", "hidden", `{"id":"person-1"}`)
			if batch {
				discovery := proposed(runner.DiscoverTools, "discover", `{"groups":["business.people"]}`)
				response.ToolCalls = append(discovery.ToolCalls, response.ToolCalls...)
			}
			out, e := build(t, d, deps(script(response))).Run(context.Background(), req(), nil)
			if !errors.Is(e, runner.ErrTool) || calls != 0 || out.ToolCalls != 0 {
				t.Fatalf("err=%v calls=%d tools=%d", e, calls, out.ToolCalls)
			}
		})
	}
}
func TestDiscoveryCapDuplicateUnloadAndReset(t *testing.T) {
	calls := 0
	d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		calls++
		return tool.Result{Status: tool.Available}, nil
	})
	turn := 0
	p := model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		turn++
		switch turn {
		case 1:
			return proposed(runner.DiscoverTools, "d1", `{"groups":["business.people"]}`), nil
		case 2:
			return proposed(runner.DiscoverTools, "d2", `{"groups":["business.people","system.permissions"]}`), nil
		case 3:
			if toolNames(r) != "usage.daily,discover_tools" || !strings.Contains(r.Messages[len(r.Messages)-1].Text, "invalid") {
				t.Fatal("cap changed active tools")
			}
			return proposed(runner.DiscoverTools, "d3", `{"groups":["business.people","business.people"]}`), nil
		case 4:
			if toolNames(r) != "usage.daily,discover_tools" {
				t.Fatal("duplicates changed active tools")
			}
			return proposed(runner.DiscoverTools, "d4", `{"groups":[]}`), nil
		default:
			if toolNames(r) != runner.DiscoverTools {
				t.Fatal("exposure leaked or unload failed")
			}
			return model.Response{FinishReason: model.Stop, Text: "done"}, nil
		}
	})
	r := build(t, d, deps(p))
	if _, e := r.Run(context.Background(), req(), nil); e != nil {
		t.Fatal(e)
	}
	if _, e := r.Run(context.Background(), req(), nil); e != nil {
		t.Fatal(e)
	}
	if calls != 0 {
		t.Fatal("discovery executed business tools")
	}
}
func TestDiscoveryRevocationBudgetAndUnknownGroup(t *testing.T) {
	for _, kind := range []string{"revoked", "tool budget", "unknown group"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				calls++
				return tool.Result{Status: tool.Available}, nil
			})
			p := script(proposed(runner.DiscoverTools, "d1", `{"groups":["business.people"]}`), proposed("usage.daily", "q1", `{"id":"person-1"}`))
			expected := runner.ErrDenied
			if kind == "tool budget" {
				d.AgentBudget.MaxToolCalls = 1
				d.AgentBudget.MaxWrites = 0
				expected = runner.ErrBudget
			}
			if kind == "unknown group" {
				p = script(proposed(runner.DiscoverTools, "d1", `{"groups":["forbidden"]}`))
				expected = tool.ErrArguments
			}
			dep := deps(p)
			checks := 0
			dep.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error {
				checks++
				if kind == "revoked" && checks > 1 {
					return errors.New("revoked")
				}
				return nil
			})
			_, e := build(t, d, dep).Run(context.Background(), req(), nil)
			if !errors.Is(e, expected) || calls != 0 {
				t.Fatalf("err=%v calls=%d", e, calls)
			}
		})
	}
}
func TestDiscoveryConfigurationAndSnapshot(t *testing.T) {
	for _, kind := range []string{"unknown", "duplicate group", "duplicate tool", "cap", "reserved", "invalid id"} {
		t.Run(kind, func(t *testing.T) {
			d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				return tool.Result{}, nil
			})
			switch kind {
			case "unknown":
				d.Discovery.Groups[0].Tools = []string{"unknown"}
			case "duplicate group":
				d.Discovery.Groups[1].ID = d.Discovery.Groups[0].ID
			case "duplicate tool":
				d.Discovery.Groups[1].Tools = []string{"usage.daily"}
			case "cap":
				d.Discovery.MaxActiveTools = 2
			case "reserved":
				d.Tools[0].Spec.Name = runner.DiscoverTools
			case "invalid id":
				d.Discovery.Groups[0].ID = "bad id"
			}
			if _, e := runner.New(d, deps(script())); !errors.Is(e, runner.ErrConfiguration) {
				t.Fatal(e)
			}
		})
	}
	var finished atomic.Int32
	d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		return tool.Result{Status: tool.Available}, nil
	})
	dep := deps(model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		if len(r.Messages) == 1 {
			if toolNames(r) != runner.DiscoverTools {
				t.Error("shared exposure")
			}
			return proposed(runner.DiscoverTools, "d1", `{"groups":["business.people"]}`), nil
		}
		if toolNames(r) != "usage.daily,discover_tools" {
			t.Error("snapshot mutated")
		}
		finished.Add(1)
		return model.Response{FinishReason: model.Stop, Text: "done"}, nil
	}))
	r := build(t, d, dep)
	d.Discovery.Groups[0].Tools[0] = "permissions.update"
	d.Discovery.Groups[0].Description = "mutated"
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := r.Run(context.Background(), req(), nil); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if finished.Load() != 12 {
		t.Fatal(finished.Load())
	}
}
func TestDiscoveredWriteContinueStillRequiresFreshAuthority(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "confirmed", true: "revoked"}[revoked], func(t *testing.T) {
			writes := 0
			d := catalogDefinition(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				writes++
				return tool.Result{Status: tool.Applied, Summary: "applied"}, nil
			})
			dep := deps(script(proposed(runner.DiscoverTools, "d1", `{"groups":["system.permissions"]}`), proposed("permissions.update", "w1", `{"id":"person-1"}`)))
			pending, e := build(t, d, dep).Run(context.Background(), req(), nil)
			if e != nil || pending.Proposal == nil || writes != 0 {
				t.Fatal(e)
			}
			dep.Approval = tool.ApprovalFunc(func(_ context.Context, p tool.Proposal) (tool.Approval, error) {
				return tool.Approval{Decision: tool.Approved, Fingerprint: p.Fingerprint, ExpiresAt: time.Now().Add(time.Minute)}, nil
			})
			dep.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error {
				if revoked {
					return errors.New("revoked")
				}
				return nil
			})
			out, e := build(t, d, dep).Continue(context.Background(), runner.Continuation{Principal: req().Principal, RunID: req().RunID, Proposal: *pending.Proposal}, nil)
			if revoked {
				if !errors.Is(e, runner.ErrDenied) || writes != 0 {
					t.Fatal(e)
				}
			} else if e != nil || out.State != runner.Completed || writes != 1 || out.ModelCalls != 0 {
				t.Fatalf("err=%v writes=%d state=%s", e, writes, out.State)
			}
		})
	}
}
