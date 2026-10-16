// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

type memberArgs struct {
	MemberID string `json:"member_id"`
	Role     string `json:"role,omitempty"`
}

func argsSchema(write bool) tool.Schema {
	s := tool.Schema{Type: "object", Properties: map[string]tool.Schema{"member_id": {Type: "string", MinLength: 1, MaxLength: 64}}, Required: []string{"member_id"}}
	if write {
		s.Properties["role"] = tool.Schema{Type: "string", Enum: []string{"employee", "analyst"}}
		s.Required = append(s.Required, "role")
	}
	return s
}
func jsonResult(value any) tool.Result {
	raw, _ := json.Marshal(value)
	return tool.Result{Status: tool.Available, Summary: string(raw)}
}
func (h *memoryHost) tools(f frame, a account, confirmed *savedPlan) ([]tool.Tool, *runner.Discovery) {
	read := func(name, description string) tool.Tool {
		return tool.Tool{Spec: tool.Spec{Name: name, Version: "1", Description: description, Effect: tool.Read, Input: argsSchema(false), Timeout: time.Second, MaxResultBytes: 2048}}
	}
	usage := read("usage.daily", "Read fictional daily usage; cost is in currency minor units, and partial does not mean published.")
	usage.Execute = func(_ context.Context, _ tool.ExecutionContext, c tool.Call, _ tool.Progress) (tool.Result, error) {
		var args memberArgs
		_ = json.Unmarshal(c.Arguments, &args)
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, e := h.targetLocked(f, args.MemberID, false); e != nil {
			return tool.Result{}, e
		}
		return jsonResult(map[string]any{"member_id": args.MemberID, "date": "2030-04-08", "input_tokens": 12000, "output_tokens": 3000, "cost_minor_units": 18, "currency": "USD", "source_state": "partial", "report_published": false}), nil
	}
	registered := []tool.Tool{usage}
	groups := []runner.ToolGroup{{ID: "business.usage", Description: "Business analysis: scoped daily usage, cost and readiness", Tools: []string{"usage.daily"}}}
	if a.Manage {
		// This map belongs only to one Run. It cannot carry a stale read into a
		// future message, or share another user's mutable authority/read state.
		observed := map[string]int{}
		memberRead := read("member.read", "Read a member's current role/version within the operator's scope before preparing a change.")
		memberRead.Execute = func(_ context.Context, _ tool.ExecutionContext, c tool.Call, _ tool.Progress) (tool.Result, error) {
			var args memberArgs
			_ = json.Unmarshal(c.Arguments, &args)
			h.mu.Lock()
			defer h.mu.Unlock()
			target, e := h.targetLocked(f, args.MemberID, true)
			if e != nil {
				return tool.Result{}, e
			}
			observed[target.ID] = target.Revision
			return jsonResult(target), nil
		}
		change := tool.Tool{Spec: tool.Spec{Name: "member.update", Version: "1", Description: "Propose one member role change after a fresh member.read. Confirmation is required; never claim it executed while pending.", Effect: tool.Write, RequiresApproval: true, Input: argsSchema(true), Timeout: time.Second, MaxResultBytes: 2048}}
		change.Prepare = func(_ context.Context, _ tool.ExecutionContext, c tool.Call) (tool.Intent, error) {
			var args memberArgs
			_ = json.Unmarshal(c.Arguments, &args)
			h.mu.Lock()
			defer h.mu.Unlock()
			target, e := h.targetLocked(f, args.MemberID, true)
			if e != nil {
				return tool.Intent{}, e
			}
			if confirmed == nil && observed[target.ID] != target.Revision {
				return tool.Intent{}, errors.New("fresh target read required")
			}
			return tool.Intent{Summary: fmt.Sprintf("Fictional member %s: %s -> %s", target.ID, target.Role, args.Role), Resources: []tool.Resource{{ID: target.ID, Version: strconv.Itoa(target.Revision)}}}, nil
		}
		change.Execute = func(_ context.Context, ec tool.ExecutionContext, c tool.Call, _ tool.Progress) (tool.Result, error) {
			var args memberArgs
			_ = json.Unmarshal(c.Arguments, &args)
			h.mu.Lock()
			defer h.mu.Unlock()
			target, e := h.targetLocked(f, args.MemberID, true)
			if e != nil {
				return tool.Result{}, e
			}
			p := h.Plans[ec.OperationID]
			if p == nil || p != confirmed || p.State != "approved" || p.Frame != f || !p.Expires.After(time.Now()) || p.Proposal.Call.Name != c.Name || string(p.Proposal.Call.Arguments) != string(c.Arguments) {
				return tool.Result{Status: tool.Conflict, Summary: "No valid unused approval; unchanged."}, nil
			}
			// This condition is enforced INSIDE the business transaction, in addition
			// to the core's pre-execution Prepare/fingerprint checks.
			resources := p.Proposal.Intent.Resources
			if len(resources) != 1 || resources[0].ID != target.ID || resources[0].Version != strconv.Itoa(target.Revision) {
				return tool.Result{Status: tool.Conflict, Summary: "Target version changed; unchanged."}, nil
			}
			before := target.Role
			target.Role = args.Role
			target.Revision++
			h.Members[target.ID] = target
			h.Writes++
			h.Audit = append(h.Audit, auditEntry{ec.OperationID, ec.Principal.SubjectID, target.ID, before, target.Role})
			p.State = "consumed"
			if h.LoseAcknowledgement {
				return tool.Result{}, errors.New("fictional acknowledgement lost after application")
			}
			return tool.Result{Status: tool.Applied, Summary: "Confirmed fictional role change applied once."}, nil
		}
		registered = append(registered, memberRead, change)
		groups = append(groups, runner.ToolGroup{ID: "system.members", Description: "System management: scoped member permission read and confirmed change", Tools: []string{"member.read", "member.update"}})
		if confirmed != nil {
			return []tool.Tool{change}, nil
		}
	}
	return registered, &runner.Discovery{Groups: groups, MaxActiveTools: 3}
}
