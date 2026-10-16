// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

var namePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.-]{0,63}$`)
var evidencePattern = regexp.MustCompile(`\[\[evidence:([^\]\s]+)\]\]`)

func validBudget(b Budget) bool {
	return b.Timeout > 0 && b.MaxModelCalls > 0 && b.MaxToolCalls > 0 && b.MaxWrites >= 0 && b.MaxWrites <= b.MaxToolCalls &&
		b.MaxInputBytes > 0 && b.MaxOutputBytes > 0 && b.MaxOutputTokens > 0 && b.MaxRepeatedReads > 0
}

func newRunner(d Definition, deps Dependencies) (*Runner, error) {
	if d.ID == "" || d.Version == "" || d.Model == "" || deps.Provider == nil || !utf8.ValidString(d.System) || !utf8.ValidString(d.ChatInstructions) || !utf8.ValidString(d.AgentInstructions) {
		return nil, ErrConfiguration
	}
	if d.ChatBudget == (Budget{}) {
		d.ChatBudget = DefaultBudget(Chat)
	}
	if d.AgentBudget == (Budget{}) {
		d.AgentBudget = DefaultBudget(Agent)
	}
	if d.ChatInstructions == "" {
		d.ChatInstructions = "Focus on the current question. Use only necessary authorized tools. Cite evidence as [[evidence:id]]."
	}
	if d.AgentInstructions == "" {
		d.AgentInstructions = "Investigate the current request in bounded steps using authorized tools. Corroborate findings; distinguish facts, uncertainty and suggestions. Cite evidence as [[evidence:id]]."
	}
	if !validBudget(d.ChatBudget) || !validBudget(d.AgentBudget) || (len(d.Tools) > 0 && deps.Authorizer == nil) {
		return nil, ErrConfiguration
	}
	if err := prepareDiscovery(&d); err != nil {
		return nil, err
	}
	registered := make(map[string]tool.Tool, len(d.Tools))
	copied := make([]tool.Tool, 0, len(d.Tools))
	for _, t := range d.Tools {
		s := t.Spec
		if !namePattern.MatchString(s.Name) || s.Version == "" || !utf8.ValidString(s.Description) || s.Timeout <= 0 || s.MaxResultBytes <= 0 ||
			(s.Effect != tool.Read && s.Effect != tool.Write) || s.Input.Type != "object" || s.Input.Check() != nil || t.Execute == nil {
			return nil, ErrConfiguration
		}
		// Every write needs a read-only Prepare. It is the only source of the
		// concrete resources, target versions and impact bound to an operation, so
		// a write without it cannot be authorized, bound or audited as one.
		if s.Effect == tool.Write && (t.Prepare == nil || deps.OperationID == nil) {
			return nil, ErrConfiguration
		}
		if _, exists := registered[s.Name]; exists {
			return nil, ErrConfiguration
		}
		b, err := json.Marshal(s.Input)
		if err != nil {
			return nil, ErrConfiguration
		}
		var snapshot tool.Schema
		if err := json.Unmarshal(b, &snapshot); err != nil {
			return nil, ErrConfiguration
		}
		t.Spec.Input = snapshot
		registered[s.Name] = t
		copied = append(copied, t)
	}
	d.Tools = copied
	return &Runner{definition: d, dependencies: deps, tools: registered}, nil
}

func cloneMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, len(messages))
	for i, m := range messages {
		out[i] = m
		out[i].ToolCalls = append([]model.ToolCall(nil), m.ToolCalls...)
		for j := range out[i].ToolCalls {
			out[i].ToolCalls[j].Arguments = append(json.RawMessage(nil), m.ToolCalls[j].Arguments...)
		}
	}
	return out
}

func (r *Runner) run(parent context.Context, req Request, handle event.Handler) (out Outcome, runErr error) {
	out.State = Interrupted
	turn := 0
	emit := func(e event.Event) error {
		e.RunID = req.RunID
		e.Turn = turn
		if handle != nil {
			if err := handle(e); err != nil {
				return &Failure{Code: "event_failed", Cause: errors.Join(ErrEvent, err)}
			}
		}
		return nil
	}
	defer func() {
		if runErr != nil {
			code := "execution_failed"
			var failure *Failure
			if errors.As(runErr, &failure) {
				code = failure.Code
			} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
				code = "cancelled_or_timed_out"
			}
			_ = emit(event.Event{Kind: event.Error, Code: code})
		}
	}()
	fail := func(code string, err error) (Outcome, error) { return out, &Failure{Code: code, Cause: err} }
	if req.Mode == "" {
		req.Mode = Chat
	}
	if req.RunID == "" || len(req.RunID) > 128 || !utf8.ValidString(req.RunID) || req.Question == "" || !utf8.ValidString(req.Question) || (req.Mode != Chat && req.Mode != Agent) {
		return fail("invalid_request", ErrRequest)
	}
	if len(r.tools) > 0 && (req.Principal.SubjectID == "" || req.Principal.TenantID == "" || len(req.Principal.SubjectID) > 256 || len(req.Principal.TenantID) > 256 || !utf8.ValidString(req.Principal.SubjectID) || !utf8.ValidString(req.Principal.TenantID)) {
		return fail("invalid_identity", ErrRequest)
	}
	b := r.definition.ChatBudget
	system := r.definition.System + "\n" + r.definition.ChatInstructions
	if req.Mode == Agent {
		b = r.definition.AgentBudget
		system = r.definition.System + "\n" + r.definition.AgentInstructions
	}
	ctx, cancel := context.WithTimeout(parent, b.Timeout)
	defer cancel()
	out.History = cloneMessages(req.History)
	for _, m := range out.History {
		if (m.Role != model.User && m.Role != model.Assistant && m.Role != model.Tool) || !utf8.ValidString(m.Text) {
			return fail("invalid_history", ErrRequest)
		}
	}
	out.History = append(out.History, model.Message{Role: model.User, Text: req.Question})
	evidence := map[string]bool{}
	for _, id := range req.EvidenceIDs {
		if id == "" || len(id) > 128 || !utf8.ValidString(id) {
			return fail("invalid_evidence", ErrRequest)
		}
		evidence[id] = true
	}
	exposure := r.exposure()
	seen := map[string]int{}
	callIDs := map[string]bool{}
	operationIDs := map[string]bool{}
	outputBytes := 0
	for turn = 1; turn <= b.MaxModelCalls; turn++ {
		if err := ctx.Err(); err != nil {
			return fail("cancelled_or_timed_out", err)
		}
		definitions := exposure.definitions(r.definition.Tools)
		visible := map[string]bool{}
		for _, d := range definitions {
			visible[d.Name] = true
		}
		mr := model.Request{Model: r.definition.Model, System: system, Messages: cloneMessages(out.History), Tools: definitions, MaxOutputTokens: b.MaxOutputTokens, MaxResponseBytes: b.MaxOutputBytes - outputBytes}
		// A fresh schema copy prevents a provider from mutating the next turn.
		mr.Tools = append([]model.ToolDefinition(nil), definitions...)
		for i := range mr.Tools {
			mr.Tools[i].Parameters = append(json.RawMessage(nil), mr.Tools[i].Parameters...)
		}
		encoded, err := json.Marshal(mr)
		inputBytes := len(encoded)
		for _, m := range mr.Messages {
			inputBytes += len(m.ReasoningContent)
		}
		if err != nil || inputBytes > b.MaxInputBytes || mr.MaxResponseBytes <= 0 {
			return fail("input_or_output_budget", ErrBudget)
		}
		out.ModelCalls++
		var preview strings.Builder
		var callbackErr error
		response, err := r.dependencies.Provider.Generate(ctx, mr, func(delta string) error {
			if callbackErr != nil {
				return callbackErr
			}
			if err := ctx.Err(); err != nil {
				callbackErr = err
				return err
			}
			if !utf8.ValidString(delta) || outputBytes+preview.Len()+len(delta) > b.MaxOutputBytes {
				callbackErr = ErrBudget
				return callbackErr
			}
			preview.WriteString(delta)
			callbackErr = emit(event.Event{Kind: event.Delta, Text: delta})
			return callbackErr
		})
		if callbackErr != nil {
			return fail("stream_stopped", callbackErr)
		}
		if err != nil {
			// A provider that surfaces cancellation as its own error must still be
			// reported as cancellation, not as a model failure.
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return fail("cancelled_or_timed_out", errors.Join(ctx.Err(), err))
			}
			return fail("model_failed", errors.Join(ErrModel, err))
		}
		if err := ctx.Err(); err != nil {
			return fail("cancelled_or_timed_out", err)
		}
		if !utf8.ValidString(response.Text) || (preview.Len() > 0 && preview.String() != response.Text) {
			return fail("invalid_model_stream", ErrModel)
		}
		encoded, err = json.Marshal(response)
		if err != nil {
			return fail("invalid_model_response", ErrModel)
		}
		if outputBytes+len(encoded) > b.MaxOutputBytes {
			return fail("output_budget", ErrBudget)
		}
		outputBytes += len(encoded)
		if response.FinishReason != model.Stop && response.FinishReason != model.ToolUse {
			return fail("incomplete_model_stream", ErrModel)
		}
		if (response.FinishReason == model.ToolUse) != (len(response.ToolCalls) > 0) {
			return fail("invalid_model_response", ErrModel)
		}
		if preview.Len() == 0 && response.Text != "" {
			if err := emit(event.Event{Kind: event.Delta, Text: response.Text}); err != nil {
				return out, err
			}
		}
		out.History = append(out.History, cloneMessages([]model.Message{{Role: model.Assistant, Text: response.Text, ReasoningContent: response.ReasoningContent, ToolCalls: response.ToolCalls}})[0])
		if len(response.ToolCalls) == 0 {
			if strings.TrimSpace(response.Text) == "" {
				return fail("empty_model_response", ErrModel)
			}
			for _, match := range evidencePattern.FindAllStringSubmatch(response.Text, -1) {
				if !evidence[match[1]] {
					return fail("invalid_evidence_reference", ErrEvidence)
				}
			}
			out.Text = response.Text
			if r.dependencies.ValidateFinal != nil {
				validation := out
				validation.History = cloneMessages(out.History)
				validation.Observations = append([]Observation(nil), out.Observations...)
				if err := r.dependencies.ValidateFinal(ctx, validation); err != nil {
					return fail("final_rejected", err)
				}
			}
			if err := ctx.Err(); err != nil {
				return fail("cancelled_or_timed_out", err)
			}
			if err := emit(event.Event{Kind: event.Done, Text: response.Text}); err != nil {
				return out, err
			}
			out.State = Completed
			return out, nil
		}
		// Validate call structure and exposure before any batch side effects.
		// Unknown/unloaded tools are observations the model can correct; they
		// never cause implicit registration, discovery or execution.
		batchIDs := map[string]bool{}
		unavailable := false
		for _, proposed := range response.ToolCalls {
			if !namePattern.MatchString(proposed.Name) || proposed.ID == "" || len(proposed.ID) > 128 || !utf8.ValidString(proposed.ID) || callIDs[proposed.ID] || batchIDs[proposed.ID] || !json.Valid(proposed.Arguments) {
				return fail("unregistered_or_invalid_tool", ErrTool)
			}
			batchIDs[proposed.ID] = true
			unavailable = unavailable || !visible[proposed.Name]
		}
		if unavailable {
			for _, proposed := range response.ToolCalls {
				if out.ToolCalls >= b.MaxToolCalls {
					return fail("tool_budget", ErrBudget)
				}
				key := proposed.Name + ":" + string(proposed.Arguments)
				seen[key]++
				if seen[key] > b.MaxRepeatedReads {
					return fail("repeated_tool", ErrLoop)
				}
				callIDs[proposed.ID] = true
				out.ToolCalls++
				result := tool.Result{Status: tool.Invalid, Code: "batch_not_executed", Summary: "No tools in this batch executed because it included an unavailable tool. Use the currently exposed schemas, or discover the required group first in a separate turn."}
				if !visible[proposed.Name] {
					result.Code = "tool_unavailable"
					result.Summary = "This tool is not available in the current tool set. Do not guess names. Use the currently exposed schemas or discover an authorized group first; if no capability exists, explain the limitation. Nothing executed."
				}
				if err := recordToolResult(&out, proposed, result, &outputBytes, b.MaxOutputBytes, emit); err != nil {
					return out, err
				}
			}
			continue
		}
		for _, proposed := range response.ToolCalls {
			if err := ctx.Err(); err != nil {
				return fail("cancelled_or_timed_out", err)
			}
			t, ok := r.tools[proposed.Name]
			if !ok || proposed.ID == "" || len(proposed.ID) > 128 || !utf8.ValidString(proposed.ID) || callIDs[proposed.ID] {
				return fail("unregistered_or_invalid_tool", ErrTool)
			}
			if r.definition.Discovery != nil && proposed.Name == DiscoverTools {
				t.Execute = exposure.load
			}
			callIDs[proposed.ID] = true
			if out.ToolCalls >= b.MaxToolCalls || (t.Spec.Effect == tool.Write && out.Writes >= b.MaxWrites) {
				return fail("tool_budget", ErrBudget)
			}
			if t.Spec.MaxResultBytes > b.MaxOutputBytes-outputBytes {
				return fail("tool_result_budget", ErrBudget)
			}
			args, err := t.Spec.Input.Normalize(proposed.Arguments)
			if err != nil {
				out.ToolCalls++
				key := proposed.Name + ":" + string(proposed.Arguments)
				seen[key]++
				if seen[key] > b.MaxRepeatedReads {
					return fail("repeated_tool", ErrLoop)
				}
				result := tool.Result{Status: tool.Invalid, Code: "invalid_arguments", Summary: "Arguments do not match this tool's exposed schema. Correct the required fields, types and allowed values; unknown or duplicate fields are rejected. Nothing executed."}
				if err := recordToolResult(&out, proposed, result, &outputBytes, b.MaxOutputBytes, emit); err != nil {
					return out, err
				}
				continue
			}
			c := tool.Call{Name: t.Spec.Name, Version: t.Spec.Version, Arguments: args}
			key := c.Name + "@" + c.Version + ":" + string(args)
			seen[key]++
			if (t.Spec.Effect == tool.Write && seen[key] > 1) || seen[key] > b.MaxRepeatedReads {
				return fail("repeated_tool", ErrLoop)
			}
			ec := tool.ExecutionContext{Principal: req.Principal, AssistantID: r.definition.ID, AssistantVersion: r.definition.Version, RunID: req.RunID, CallID: proposed.ID}
			toolCtx, stop := context.WithTimeout(ctx, t.Spec.Timeout)
			// Keep the full chain within the tool deadline, including host callbacks.
			result, proposal, err := r.execute(toolCtx, ec, c, t, emit, &out, operationIDs)
			stop()
			if proposal != nil {
				out.State = PendingApproval
				out.Proposal = proposal
				return out, nil
			}
			if err != nil {
				return out, err
			}
			encoded, _ := json.Marshal(result)
			if outputBytes+len(encoded) > b.MaxOutputBytes {
				return fail("output_budget", ErrBudget)
			}
			outputBytes += len(encoded)
			if result.EvidenceID != "" {
				evidence[result.EvidenceID] = true
			}
			out.History = append(out.History, model.Message{Role: model.Tool, ToolCallID: proposed.ID, Text: string(encoded)})
		}
	}
	return fail("model_budget", ErrBudget)
}

// recordToolResult closes a rejected call without invoking host callbacks. It
// shares the normal output budget and persists a public diagnostic observation.
func recordToolResult(out *Outcome, c model.ToolCall, result tool.Result, used *int, limit int, emit func(event.Event) error) error {
	encoded, err := json.Marshal(result)
	if err != nil || *used+len(encoded) > limit {
		return &Failure{Code: "output_budget", Cause: ErrBudget}
	}
	*used += len(encoded)
	out.Observations = append(out.Observations, Observation{CallID: c.ID, Tool: c.Name, Result: result})
	if err := emit(event.Event{Kind: event.ToolEnd, Tool: c.Name, CallID: c.ID, Status: result.Status, Code: result.Code}); err != nil {
		return err
	}
	out.History = append(out.History, model.Message{Role: model.Tool, ToolCallID: c.ID, Text: string(encoded)})
	return nil
}

func fingerprint(ec tool.ExecutionContext, c tool.Call, intent tool.Intent) string {
	return tool.ProposalFingerprint(ec, c, intent)
}

func copyCall(c tool.Call) tool.Call {
	c.Arguments = append(json.RawMessage(nil), c.Arguments...)
	return c
}

func (r *Runner) execute(ctx context.Context, ec tool.ExecutionContext, c tool.Call, t tool.Tool, emit func(event.Event) error, out *Outcome, operationIDs map[string]bool) (tool.Result, *tool.Proposal, error) {
	fail := func(code string, err error) (tool.Result, *tool.Proposal, error) {
		return tool.Result{}, nil, &Failure{Code: code, Cause: err}
	}
	if err := r.dependencies.Authorizer.CheckTool(ctx, ec, copyCall(c)); err != nil {
		return fail("tool_denied", errors.Join(ErrDenied, err))
	}
	if t.Spec.Effect == tool.Write {
		id, err := r.dependencies.OperationID(ctx, ec, copyCall(c))
		if err != nil || id == "" || len(id) > 128 || !utf8.ValidString(id) || operationIDs[id] {
			return fail("invalid_operation_id", ErrRequest)
		}
		ec.OperationID = id
	}
	// Writes always produce an intent, whether or not a human approval is
	// required: it is what binds the operation to its resources and target
	// versions, and what the post-approval re-check compares against.
	if t.Spec.Effect == tool.Write {
		intent, err := t.Prepare(ctx, ec, copyCall(c))
		if err != nil || !validIntent(intent) {
			return fail("invalid_operation_proposal", ErrApproval)
		}
		p := tool.Proposal{Principal: ec.Principal, RunID: ec.RunID, OperationID: ec.OperationID, Call: copyCall(c), Intent: intent, Fingerprint: fingerprint(ec, c, intent)}
		var expiresAt time.Time
		if t.Spec.RequiresApproval {
			decision := tool.Approval{Decision: tool.Pending}
			if r.dependencies.Approval != nil {
				gateProposal := p
				gateProposal.Call = copyCall(p.Call)
				gateProposal.Intent.Resources = append([]tool.Resource(nil), p.Intent.Resources...)
				decision, err = r.dependencies.Approval.Approve(ctx, gateProposal)
				if err != nil {
					return fail("approval_failed", errors.Join(ErrApproval, err))
				}
			}
			if err := ctx.Err(); err != nil {
				return fail("approval_timed_out", err)
			}
			if decision.Decision == tool.Pending {
				view := &event.ApprovalView{Tool: c.Name, OperationID: ec.OperationID, Summary: intent.Summary, Resources: append([]tool.Resource(nil), intent.Resources...), Fingerprint: p.Fingerprint}
				if err := emit(event.Event{Kind: event.ApprovalRequired, Tool: c.Name, CallID: ec.CallID, Approval: view}); err != nil {
					return fail("event_failed", err)
				}
				return tool.Result{}, &p, nil
			}
			if decision.Decision != tool.Approved || decision.Fingerprint != p.Fingerprint || !decision.ExpiresAt.After(time.Now()) {
				return fail("approval_rejected", ErrApproval)
			}
			expiresAt = decision.ExpiresAt
		}
		if err := r.dependencies.Authorizer.CheckTool(ctx, ec, copyCall(c)); err != nil {
			return fail("tool_denied", errors.Join(ErrDenied, err))
		}
		latest, err := t.Prepare(ctx, ec, copyCall(c))
		if err != nil || !validIntent(latest) || fingerprint(ec, c, latest) != p.Fingerprint {
			return fail("proposal_changed", ErrApproval)
		}
		// An approval must still be valid after the re-check, not just when read.
		if t.Spec.RequiresApproval && !expiresAt.After(time.Now()) {
			return fail("proposal_changed", ErrApproval)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail("tool_cancelled", err)
	}
	if err := emit(event.Event{Kind: event.ToolStart, Tool: c.Name, CallID: ec.CallID}); err != nil {
		return fail("event_failed", err)
	}
	if err := ctx.Err(); err != nil {
		return fail("tool_cancelled", err)
	}
	out.ToolCalls++
	if t.Spec.Effect == tool.Write {
		out.Writes++
		operationIDs[ec.OperationID] = true
	}
	var progressErr error
	progressBytes := 0
	result, err := t.Execute(ctx, ec, copyCall(c), func(message string) error {
		if progressErr != nil {
			return progressErr
		}
		progressBytes += len(message)
		if ctx.Err() != nil {
			progressErr = ctx.Err()
		} else if !utf8.ValidString(message) || progressBytes > t.Spec.MaxResultBytes {
			progressErr = ErrBudget
		} else {
			progressErr = emit(event.Event{Kind: event.ToolProgress, Tool: c.Name, CallID: ec.CallID, Text: message})
		}
		return progressErr
	})
	resultBytes, marshalErr := json.Marshal(result)
	invalid := marshalErr != nil || !result.Valid(t.Spec.Effect) || !utf8.ValidString(result.Summary) || !utf8.ValidString(result.EvidenceID) || len(result.EvidenceID) > 128 || len(resultBytes) > t.Spec.MaxResultBytes
	if err != nil || ctx.Err() != nil || invalid {
		if t.Spec.Effect == tool.Write {
			result = tool.Result{Status: tool.Unknown, Summary: "Operation outcome is unconfirmed; query the host operation record before continuing."}
		} else {
			result = tool.Result{Status: tool.Unavailable, Code: "tool_execution_failed", Summary: "Read failed, timed out, or returned an invalid result. No usable evidence was obtained. Correct the query, consult another authorized source, or explain the missing evidence; do not invent a result."}
		}
	}
	out.Observations = append(out.Observations, Observation{CallID: ec.CallID, Tool: c.Name, OperationID: ec.OperationID, Result: result})
	if endErr := emit(event.Event{Kind: event.ToolEnd, Tool: c.Name, CallID: ec.CallID, Status: result.Status, Code: result.Code}); endErr != nil {
		return fail("event_failed", endErr)
	}
	if result.Status == tool.Unknown || result.Status == tool.Partial {
		return fail("operation_unconfirmed", ErrUncertain)
	}
	if progressErr != nil {
		return fail("progress_stopped", progressErr)
	}
	// Read failures (including a tool-local deadline) are observations. The
	// outer loop still enforces the run/parent deadline before any next call.
	// Unknown writes, progress failures and authorization remain terminal.
	return result, nil, nil
}

func validIntent(intent tool.Intent) bool {
	if intent.Summary == "" || len(intent.Summary) > 4096 || !utf8.ValidString(intent.Summary) || len(intent.Resources) == 0 || len(intent.Resources) > 32 {
		return false
	}
	for _, resource := range intent.Resources {
		if resource.ID == "" || len(resource.ID) > 256 || len(resource.Version) > 256 || !utf8.ValidString(resource.ID) || !utf8.ValidString(resource.Version) {
			return false
		}
	}
	return true
}

// Describe returns a safe identifier for host logs, never a model prompt or credentials.
func (r *Runner) Describe() string {
	return fmt.Sprintf("%s@%s", r.definition.ID, r.definition.Version)
}
