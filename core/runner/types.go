// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package runner implements a bounded, serial model/tool loop. Authentication,
// storage, scheduling, approval persistence and side-effect deduplication are host concerns.
package runner

import (
	"context"
	"errors"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

type Mode string

const (
	Chat  Mode = "chat"
	Agent Mode = "agent"
)

type Budget struct {
	Timeout          time.Duration
	MaxModelCalls    int
	MaxToolCalls     int
	MaxWrites        int
	MaxInputBytes    int
	MaxOutputBytes   int
	MaxOutputTokens  int
	MaxRepeatedReads int
}

func DefaultBudget(mode Mode) Budget {
	b := Budget{Timeout: 90 * time.Second, MaxModelCalls: 8, MaxToolCalls: 12,
		MaxInputBytes: 128 << 10, MaxOutputBytes: 64 << 10, MaxOutputTokens: 4096, MaxRepeatedReads: 2}
	if mode == Agent {
		b.Timeout = 3 * time.Minute
		b.MaxModelCalls = 16
		b.MaxToolCalls = 24
	}
	return b
}

type Definition struct {
	ID                string
	Version           string
	System            string
	ChatInstructions  string
	AgentInstructions string
	Model             string
	Tools             []tool.Tool
	Discovery         *Discovery
	ChatBudget        Budget
	AgentBudget       Budget
}

type Dependencies struct {
	Provider   model.Provider
	Authorizer tool.Authorizer
	Approval   tool.ApprovalGate
	// OperationID binds each write to an existing host intent. It must remain
	// stable on explicit continuation, differ for a new intent and never come from the model.
	OperationID func(context.Context, tool.ExecutionContext, tool.Call) (string, error)
	// ValidateFinal can apply host-specific evidence/output policy before done.
	ValidateFinal func(context.Context, Outcome) error
}

type Request struct {
	Principal tool.Principal
	RunID     string
	Mode      Mode
	History   []model.Message
	Question  string
	// EvidenceIDs are trusted host-authorized references already in history.
	EvidenceIDs []string
}

type State string

const (
	Completed       State = "completed"
	PendingApproval State = "pending_approval"
	Interrupted     State = "interrupted"
)

type Observation struct {
	CallID      string
	Tool        string
	OperationID string
	Result      tool.Result
}

type Outcome struct {
	State        State
	Text         string
	History      []model.Message
	Observations []Observation
	Proposal     *tool.Proposal
	ModelCalls   int
	ToolCalls    int
	Writes       int
}

var (
	ErrConfiguration = errors.New("invalid configuration")
	ErrRequest       = errors.New("invalid request")
	ErrBudget        = errors.New("budget exhausted")
	ErrLoop          = errors.New("repeated tool call")
	ErrDenied        = errors.New("tool denied")
	ErrApproval      = errors.New("approval invalid or rejected")
	ErrModel         = errors.New("model failed or incomplete")
	ErrTool          = errors.New("tool failed or invalid result")
	ErrUncertain     = errors.New("operation not fully confirmed")
	ErrEvidence      = errors.New("unrecognized evidence reference")
	ErrEvent         = errors.New("event handler failed")
)

// Failure's printable form excludes raw provider/tool/host error text.
// Hosts may inspect Unwrap for protected diagnostics, never expose it blindly.
type Failure struct {
	Code  string
	Cause error
}

func (f *Failure) Error() string { return f.Code }
func (f *Failure) Unwrap() error { return f.Cause }

type Runner struct {
	definition   Definition
	dependencies Dependencies
	tools        map[string]tool.Tool
}

// New snapshots definitions and schemas. Callbacks/Provider must be concurrency
// safe if one Runner serves concurrent executions; per-run state stays local.
func New(d Definition, deps Dependencies) (*Runner, error) {
	return newRunner(d, deps)
}

func (r *Runner) Run(ctx context.Context, req Request, handle event.Handler) (Outcome, error) {
	return r.run(ctx, req, handle)
}
