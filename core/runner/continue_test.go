// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner_test

import (
	"context"
	"errors"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
	"testing"
)

func TestContinueExactApprovedProposalWithoutModel(t *testing.T) {
	for _, tc := range []string{"approved", "actor", "tenant", "run", "arguments", "version", "intent", "operation", "resource changed", "revoked", "unknown", "pending", "cancelled"} {
		t.Run(tc, func(t *testing.T) {
			writes, models := 0, 0
			wt := write(func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
				writes++
				if tc == "unknown" {
					return tool.Result{Status: tool.Unknown, Summary: "unconfirmed"}, nil
				}
				return tool.Result{Status: tool.Applied, Summary: "configured"}, nil
			})
			dep := deps(model.ProviderFunc(func(ctx context.Context, r model.Request, _ func(string) error) (model.Response, error) {
				models++
				return proposed("job.retry", "c1", `{"id":"job-1"}`), nil
			}))
			r := build(t, definition(wt), dep)
			out, e := r.Run(t.Context(), req(), nil)
			if e != nil || out.State != runner.PendingApproval || out.Proposal == nil || writes != 0 {
				t.Fatal(out, e)
			}
			p := *out.Proposal
			cr := runner.Continuation{Principal: p.Principal, RunID: p.RunID, Proposal: p}
			dep.Provider = model.ProviderFunc(func(context.Context, model.Request, func(string) error) (model.Response, error) {
				t.Fatal("continuation called model")
				return model.Response{}, nil
			})
			dep.Approval = approve()
			switch tc {
			case "actor":
				cr.Principal.SubjectID = "other"
			case "tenant":
				cr.Principal.TenantID = "other"
			case "run":
				cr.RunID = "other"
			case "arguments":
				cr.Proposal.Call.Arguments = []byte(`{"id":"other"}`)
			case "version":
				wt.Spec.Version = "2"
			case "intent":
				cr.Proposal.Intent.Summary = "different"
			case "operation":
				dep.OperationID = func(context.Context, tool.ExecutionContext, tool.Call) (string, error) { return "other", nil }
			case "resource changed":
				wt.Prepare = func(context.Context, tool.ExecutionContext, tool.Call) (tool.Intent, error) {
					return tool.Intent{Summary: p.Intent.Summary, Resources: []tool.Resource{{ID: "job-1", Version: "v2"}}}, nil
				}
			case "revoked":
				dep.Authorizer = tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error { return errors.New("revoked") })
			case "pending":
				dep.Approval = tool.ApprovalFunc(func(context.Context, tool.Proposal) (tool.Approval, error) {
					return tool.Approval{Decision: tool.Pending}, nil
				})
			}
			ctx := t.Context()
			if tc == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			r = build(t, definition(wt), dep)
			got, e := r.Continue(ctx, cr, nil)
			if models != 1 {
				t.Fatal("model count", models)
			}
			if tc == "approved" {
				if e != nil || got.State != runner.Completed || writes != 1 || got.ModelCalls != 0 {
					t.Fatal(got, e, writes)
				}
			} else if tc == "unknown" {
				if !errors.Is(e, runner.ErrUncertain) || writes != 1 {
					t.Fatal(got, e)
				}
			} else if tc == "pending" {
				if e != nil || got.State != runner.PendingApproval || writes != 0 {
					t.Fatal(got, e)
				}
			} else if e == nil || writes != 0 {
				t.Fatal("unsafe continuation", got, e, writes)
			}
		})
	}
}
