// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

// Continuation identifies a host-persisted proposal. It must be loaded by the
// authenticated host, never accepted as an arbitrary client/model JSON object.
// Approval authentication, expiry, one-time consumption, transactional execution
// and deduplication remain host responsibilities.
type Continuation struct {
	Principal tool.Principal
	RunID     string
	Proposal  tool.Proposal
}

// Continue executes exactly one approved proposal, with no model call or history
// replay. It rechecks the registered schema/version, identity, operation binding,
// authorization, approval and current resource versions using the same execution
// path as Run. An uncertain outcome is never automatically retried.
func (r *Runner) Continue(parent context.Context, req Continuation, handle event.Handler) (out Outcome, runErr error) {
	out.State = Interrupted
	fail := func(code string, err error) (Outcome, error) { return out, &Failure{Code: code, Cause: err} }
	emit := func(e event.Event) error {
		e.RunID = req.RunID
		if handle != nil {
			if err := handle(e); err != nil {
				return &Failure{Code: "event_failed", Cause: errors.Join(ErrEvent, err)}
			}
		}
		return nil
	}
	defer func() {
		if runErr != nil {
			code := "continuation_failed"
			var f *Failure
			if errors.As(runErr, &f) {
				code = f.Code
			}
			_ = emit(event.Event{Kind: event.Error, Code: code})
		}
	}()
	p := req.Proposal
	if req.RunID == "" || len(req.RunID) > 128 || !utf8.ValidString(req.RunID) || req.RunID != p.RunID || req.Principal != p.Principal || req.Principal.SubjectID == "" || req.Principal.TenantID == "" || len(req.Principal.SubjectID) > 256 || len(req.Principal.TenantID) > 256 || !utf8.ValidString(req.Principal.SubjectID) || !utf8.ValidString(req.Principal.TenantID) {
		return fail("invalid_continuation_identity", ErrRequest)
	}
	t, ok := r.tools[p.Call.Name]
	b := r.definition.AgentBudget
	if !ok || t.Spec.Effect != tool.Write || !t.Spec.RequiresApproval || t.Spec.Version != p.Call.Version || b.MaxWrites < 1 || t.Spec.MaxResultBytes > b.MaxOutputBytes || r.dependencies.Approval == nil || !validIntent(p.Intent) || p.OperationID == "" || len(p.OperationID) > 128 || !utf8.ValidString(p.OperationID) {
		return fail("invalid_continuation", ErrApproval)
	}
	if len(p.Call.Arguments) > b.MaxInputBytes {
		return fail("input_or_output_budget", ErrBudget)
	}
	args, err := t.Spec.Input.Normalize(p.Call.Arguments)
	if err != nil {
		return fail("invalid_arguments", err)
	}
	c := tool.Call{Name: p.Call.Name, Version: p.Call.Version, Arguments: args}
	ec := tool.ExecutionContext{Principal: req.Principal, AssistantID: r.definition.ID, AssistantVersion: r.definition.Version, RunID: req.RunID, CallID: "confirmed-operation", OperationID: p.OperationID}
	if fingerprint(ec, c, p.Intent) != p.Fingerprint {
		return fail("continuation_changed", ErrApproval)
	}
	ctx, stop := context.WithTimeout(parent, b.Timeout)
	defer stop()
	bounded, cancel := context.WithTimeout(ctx, t.Spec.Timeout)
	defer cancel()
	copyRunner := *r
	// Bind again after the host's fresh read-only preparation, before approval.
	copyRunner.dependencies.OperationID = func(ctx context.Context, ec tool.ExecutionContext, c tool.Call) (string, error) {
		id, e := r.dependencies.OperationID(ctx, ec, c)
		if e != nil || id != p.OperationID {
			return "", ErrApproval
		}
		return id, nil
	}
	copyRunner.dependencies.Approval = tool.ApprovalFunc(func(ctx context.Context, current tool.Proposal) (tool.Approval, error) {
		if current.Fingerprint != p.Fingerprint || current.OperationID != p.OperationID {
			return tool.Approval{}, ErrApproval
		}
		return r.dependencies.Approval.Approve(ctx, current)
	})
	result, pending, err := copyRunner.execute(bounded, ec, c, t, emit, &out, map[string]bool{})
	if err != nil {
		return out, err
	}
	if pending != nil {
		out.State = PendingApproval
		out.Proposal = pending
		return out, nil
	}
	out.Text = result.Summary
	if r.dependencies.ValidateFinal != nil {
		if err = r.dependencies.ValidateFinal(ctx, out); err != nil {
			return fail("final_rejected", err)
		}
	}
	if err = emit(event.Event{Kind: event.Done, Text: out.Text}); err != nil {
		return out, err
	}
	out.State = Completed
	return out, nil
}
