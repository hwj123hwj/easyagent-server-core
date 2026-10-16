// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// All identities, records and approvals in this package are fictional.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

var errSession = errors.New("session or authority changed")

type account struct {
	Principal       tool.Principal
	Enabled, Manage bool
	Department      string
	Revision        int
}
type member struct {
	ID         string `json:"member_id"`
	Department string `json:"department"`
	Role       string `json:"role"`
	Revision   int    `json:"revision"`
}

type sessionKey struct {
	Principal tool.Principal
	Channel   string
}
type session struct {
	Epoch             int
	History           []model.Message
	AuthorityRevision int
}
type frame struct {
	Key                      sessionKey
	Epoch, AuthorityRevision int
	RunID                    string
}
type savedPlan struct {
	Frame    frame
	Proposal tool.Proposal
	Expires  time.Time
	State    string
	Outcome  runner.Outcome
	Err      error
	Busy     bool
}
type auditEntry struct{ OperationID, Actor, Target, Before, After string }

// memoryHost is a teaching host, not a durable queue, identity provider or IM
// server. Its mutex represents a SINGLE-PROCESS business transaction only.
type memoryHost struct {
	mu                  sync.Mutex
	Accounts            map[string]account // sender identifiers supplied by a trusted transport
	Members             map[string]member
	Sessions            map[sessionKey]session
	Plans               map[string]*savedPlan
	Busy                map[sessionKey]string
	Audit               []auditEntry
	Sequence, Writes    int
	LoseAcknowledgement bool // fixture fault injection, never a model parameter
}

func newMemoryHost() *memoryHost {
	return &memoryHost{Busy: map[sessionKey]string{},
		Accounts: map[string]account{
			"operator-demo": {Principal: tool.Principal{TenantID: "tenant-demo", SubjectID: "operator-demo"}, Enabled: true, Manage: true, Department: "team-a", Revision: 1},
			"employee-demo": {Principal: tool.Principal{TenantID: "tenant-demo", SubjectID: "member-a"}, Enabled: true, Department: "", Revision: 1},
		},
		Members:  map[string]member{"member-a": {ID: "member-a", Department: "team-a", Role: "employee", Revision: 1}, "member-b": {ID: "member-b", Department: "team-b", Role: "employee", Revision: 1}},
		Sessions: map[sessionKey]session{}, Plans: map[string]*savedPlan{},
	}
}
func (h *memoryHost) identityLocked(sender string) (account, error) {
	a, ok := h.Accounts[sender]
	if !ok || !a.Enabled {
		return account{}, runner.ErrDenied
	}
	return a, nil
}
func (h *memoryHost) currentLocked(f frame) (account, error) {
	var actor account
	found := false
	for _, a := range h.Accounts {
		if a.Principal == f.Key.Principal && a.Enabled {
			actor = a
			found = true
			break
		}
	}
	s, ok := h.Sessions[f.Key]
	if !found || !ok || s.Epoch != f.Epoch || s.AuthorityRevision != f.AuthorityRevision || actor.Revision != f.AuthorityRevision {
		return account{}, errSession
	}
	return actor, nil
}
func (h *memoryHost) targetLocked(f frame, id string, manage bool) (member, error) {
	a, e := h.currentLocked(f)
	if e != nil {
		return member{}, e
	}
	target, ok := h.Members[id]
	if !ok || a.Principal.TenantID != "tenant-demo" || manage && !a.Manage || id != a.Principal.SubjectID && (a.Department == "" || a.Department != target.Department) {
		return member{}, runner.ErrDenied
	}
	return target, nil
}
func (h *memoryHost) authorize(f frame) tool.Authorizer {
	return tool.AuthorizerFunc(func(ctx context.Context, ec tool.ExecutionContext, c tool.Call) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if ec.Principal != f.Key.Principal || ec.RunID != f.RunID {
			return runner.ErrDenied
		}
		if _, e := h.currentLocked(f); e != nil {
			return e
		}
		if c.Name == runner.DiscoverTools {
			return nil
		}
		var args memberArgs
		if json.Unmarshal(c.Arguments, &args) != nil {
			return tool.ErrArguments
		}
		switch c.Name {
		case "usage.daily":
			_, e := h.targetLocked(f, args.MemberID, false)
			return e
		case "member.read", "member.update":
			_, e := h.targetLocked(f, args.MemberID, true)
			return e
		default:
			return runner.ErrDenied
		}
	})
}
func exampleBudget() runner.Budget {
	b := runner.DefaultBudget(runner.Agent)
	b.Timeout = 10 * time.Second
	b.MaxWrites = 1
	return b
}
func (h *memoryHost) newRunner(f frame, provider model.Provider, plan *savedPlan) (*runner.Runner, error) {
	h.mu.Lock()
	a, e := h.currentLocked(f)
	h.mu.Unlock()
	if e != nil {
		return nil, e
	}
	tools, discovery := h.tools(f, a, plan)
	deps := runner.Dependencies{Provider: provider, Authorizer: h.authorize(f), OperationID: func(context.Context, tool.ExecutionContext, tool.Call) (string, error) {
		return "intent-" + f.RunID, nil
	}}
	if plan != nil {
		// A gate never authenticates a user from model text. The host already loaded
		// a saved plan and verified the deciding sender, channel, epoch and expiry.
		deps.Approval = tool.ApprovalFunc(func(_ context.Context, p tool.Proposal) (tool.Approval, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			row := h.Plans[p.OperationID]
			if row == nil || row != plan || row.State != "approved" || p.Fingerprint != row.Proposal.Fingerprint || !row.Expires.After(time.Now()) {
				return tool.Approval{}, runner.ErrApproval
			}
			return tool.Approval{Decision: tool.Approved, Fingerprint: p.Fingerprint, ExpiresAt: row.Expires}, nil
		})
	}
	budget := exampleBudget()
	if !a.Manage {
		budget.MaxWrites = 0
	}
	return runner.New(runner.Definition{ID: "example-console", Version: "1", Model: "offline-fixture", System: "Use current authorized facts. A partial source is not a published report. Preserve currency and units. Load the needed tool group before calling it; preserve the conversation across groups. Writes require a fresh read and user approval.", Tools: tools, Discovery: discovery, AgentBudget: budget}, deps)
}

type turnResult struct {
	Outcome    runner.Outcome
	ApprovalID string
}

// sender and channel MUST come from an authenticated transport/login adapter.
// In this offline program they are fixture identifiers, not secure credentials.
func (h *memoryHost) message(ctx context.Context, sender, channel, question string, provider model.Provider) (turnResult, error) {
	h.mu.Lock()
	a, e := h.identityLocked(sender)
	if e != nil {
		h.mu.Unlock()
		return turnResult{}, e
	}
	key := sessionKey{a.Principal, channel}
	if h.Busy[key] != "" {
		h.mu.Unlock()
		return turnResult{}, errors.New("conversation busy")
	}
	s := h.Sessions[key]
	if s.AuthorityRevision != a.Revision {
		s = session{Epoch: s.Epoch + 1, AuthorityRevision: a.Revision}
	}
	h.Sequence++
	f := frame{Key: key, Epoch: s.Epoch, AuthorityRevision: a.Revision, RunID: fmt.Sprint(h.Sequence)}
	h.Sessions[key] = s
	h.Busy[key] = f.RunID
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.Busy[key] == f.RunID {
			delete(h.Busy, key)
		}
	}()
	history := publicHistory(s.History)
	h.mu.Unlock()
	r, e := h.newRunner(f, provider, nil)
	if e != nil {
		return turnResult{}, e
	}
	out, e := r.Run(ctx, runner.Request{Principal: a.Principal, RunID: f.RunID, Mode: runner.Agent, History: history, Question: question}, nil)
	if e != nil {
		return turnResult{Outcome: out}, e
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Publication fence: a late answer may not reappear after reset/revocation.
	if _, e = h.currentLocked(f); e != nil {
		return turnResult{}, e
	}
	result := turnResult{Outcome: out}
	if out.State == runner.PendingApproval {
		if out.Proposal == nil {
			return turnResult{}, runner.ErrApproval
		}
		p := copyProposal(*out.Proposal)
		id := p.OperationID
		h.Plans[id] = &savedPlan{Frame: f, Proposal: p, Expires: time.Now().Add(5 * time.Minute), State: "pending"}
		result.ApprovalID = id
		s.History = closePendingCalls(out.History)
		s.History = append(s.History, model.Message{Role: model.Assistant, Text: "Preview only; not executed: " + p.Intent.Summary})
	} else {
		s.History = out.History
	}
	s.History = publicHistory(s.History)
	h.Sessions[key] = s
	return result, nil
}

// The public decision contains only an opaque host plan ID and an authenticated
// user's choice. Tool name, parameters and resource versions are loaded by host.
func (h *memoryHost) decide(ctx context.Context, sender, channel, id string, confirm bool) (runner.Outcome, error) {
	h.mu.Lock()
	a, e := h.identityLocked(sender)
	if e != nil {
		h.mu.Unlock()
		return runner.Outcome{State: runner.Interrupted}, e
	}
	p := h.Plans[id]
	if p == nil || p.Frame.Key != (sessionKey{a.Principal, channel}) {
		h.mu.Unlock()
		return runner.Outcome{State: runner.Interrupted}, runner.ErrDenied
	}
	if _, e = h.currentLocked(p.Frame); e != nil {
		h.mu.Unlock()
		return runner.Outcome{State: runner.Interrupted}, e
	}
	if p.State == "applied" || p.State == "unknown" {
		out, err := publicOutcome(p.Outcome), p.Err
		h.mu.Unlock()
		return out, err
	}
	if p.State != "pending" || p.Busy || h.Busy[p.Frame.Key] != "" || !p.Expires.After(time.Now()) {
		h.mu.Unlock()
		return runner.Outcome{State: runner.Interrupted}, runner.ErrApproval
	}
	if !confirm {
		p.State = "cancelled"
		h.decisionHistoryLocked(p, "Cancel", "Cancelled; no change executed.")
		h.mu.Unlock()
		return runner.Outcome{State: runner.Completed, Text: "Cancelled; no change executed."}, nil
	}
	p.State = "approved"
	p.Busy = true
	busyID := "confirm-" + p.Proposal.OperationID
	h.Busy[p.Frame.Key] = busyID
	defer func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.Busy[p.Frame.Key] == busyID {
			delete(h.Busy, p.Frame.Key)
		}
	}()
	h.mu.Unlock()
	// Continue does not call the model. This sentinel would fail if it did.
	provider := model.ProviderFunc(func(context.Context, model.Request, func(string) error) (model.Response, error) {
		return model.Response{}, errors.New("unexpected confirmation model call")
	})
	r, e := h.newRunner(p.Frame, provider, p)
	out := runner.Outcome{State: runner.Interrupted}
	if e == nil {
		out, e = r.Continue(ctx, runner.Continuation{Principal: a.Principal, RunID: p.Frame.RunID, Proposal: copyProposal(p.Proposal)}, nil)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p.Busy = false
	p.Outcome = publicOutcome(out)
	p.Err = e
	switch {
	case errors.Is(e, runner.ErrUncertain):
		p.State = "unknown"
	case e != nil:
		p.State = "blocked"
	case out.State == runner.Completed && len(out.Observations) == 1 && out.Observations[0].Result.Status == tool.Applied:
		p.State = "applied"
	default:
		p.State = "blocked"
	}
	text := out.Text
	if e != nil {
		text = "Change blocked; query current state before preparing a new proposal."
	}
	if errors.Is(e, runner.ErrUncertain) {
		text = "Operation outcome is unconfirmed; inspect the host record before retrying."
	}
	h.decisionHistoryLocked(p, "Confirm", text)
	// Never re-run a failed/unknown intent to regenerate a reply. A production
	// host persists execution + audit + outcome/outbox with its own transaction.
	return out, e
}
func (h *memoryHost) reset(sender, channel string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	a, e := h.identityLocked(sender)
	if e != nil {
		return e
	}
	key := sessionKey{a.Principal, channel}
	s := h.Sessions[key]
	h.Sessions[key] = session{Epoch: s.Epoch + 1, AuthorityRevision: a.Revision}
	return nil
}
func copyProposal(p tool.Proposal) tool.Proposal {
	raw, _ := json.Marshal(p)
	var copy tool.Proposal
	_ = json.Unmarshal(raw, &copy)
	return copy
}
func publicHistory(history []model.Message) []model.Message {
	raw, _ := json.Marshal(history)
	var copy []model.Message
	_ = json.Unmarshal(raw, &copy)
	// Keep whole user turns and their tool-call/result pairs. Private provider
	// continuation fields have json:"-" and never enter stored/public history.
	for len(copy) > 0 {
		raw, _ = json.Marshal(copy)
		if len(raw) <= 24<<10 && len(copy) <= 60 {
			break
		}
		next := 0
		for i := 1; i < len(copy); i++ {
			if copy[i].Role == model.User {
				next = i
				break
			}
		}
		if next == 0 {
			return nil
		}
		copy = copy[next:]
	}
	return copy
}
func closePendingCalls(history []model.Message) []model.Message {
	out := publicHistory(history)
	answered := map[string]bool{}
	for _, m := range out {
		if m.Role == model.Tool {
			answered[m.ToolCallID] = true
		}
	}
	for _, m := range out {
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				raw, _ := json.Marshal(tool.Result{Status: tool.Unavailable, Summary: "Not executed; this turn is waiting for user approval."})
				out = append(out, model.Message{Role: model.Tool, ToolCallID: c.ID, Text: string(raw)})
			}
		}
	}
	return out
}

func (h *memoryHost) decisionHistoryLocked(p *savedPlan, choice, text string) {
	if _, e := h.currentLocked(p.Frame); e != nil {
		return
	}
	s := h.Sessions[p.Frame.Key]
	s.History = publicHistory(append(s.History, model.Message{Role: model.User, Text: choice + " " + p.Proposal.OperationID}, model.Message{Role: model.Assistant, Text: text}))
	h.Sessions[p.Frame.Key] = s
}

func publicOutcome(out runner.Outcome) runner.Outcome {
	raw, _ := json.Marshal(out)
	var copy runner.Outcome
	_ = json.Unmarshal(raw, &copy)
	return copy
}
