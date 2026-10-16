// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package tool defines explicitly registered host callbacks. It has no built-in
// execution tools, database connections or external code loading facility.
package tool

import (
	"context"
	"encoding/json"
	"time"
)

type Effect string

const (
	Read  Effect = "read"
	Write Effect = "write"
)

type Principal struct {
	TenantID  string `json:"tenant_id"`
	SubjectID string `json:"subject_id"`
}

type ExecutionContext struct {
	Principal        Principal
	AssistantID      string
	AssistantVersion string
	RunID            string
	CallID           string
	OperationID      string
}

type Spec struct {
	Name             string
	Version          string
	Description      string
	Effect           Effect
	Input            Schema
	RequiresApproval bool
	Timeout          time.Duration
	MaxResultBytes   int
}

type Call struct {
	Name      string          `json:"name"`
	Version   string          `json:"version"`
	Arguments json.RawMessage `json:"arguments"`
}

type Status string

const (
	Available   Status = "available"
	Missing     Status = "missing"
	Denied      Status = "denied"
	Invalid     Status = "invalid"
	Unavailable Status = "unavailable"
	Applied     Status = "applied"
	Conflict    Status = "conflict"
	Partial     Status = "partial"
	Unknown     Status = "unknown"
)

type Result struct {
	// Code is a safe, machine-readable outcome category, never a raw host error.
	Code       string `json:"code,omitempty"`
	Status     Status `json:"status"`
	EvidenceID string `json:"evidence_id,omitempty"`
	Summary    string `json:"summary"`
}

func (r Result) Valid(effect Effect) bool {
	switch r.Status {
	case Denied, Invalid, Unavailable:
		return true
	case Available, Missing:
		return effect == Read
	case Applied, Conflict, Partial, Unknown:
		return effect == Write
	default:
		return false
	}
}

// Intent is produced by a read-only host preparation callback. Summary and
// resources must be safe to display; target versions are checked again after approval.
type Resource struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}
type Intent struct {
	Summary   string     `json:"summary"`
	Resources []Resource `json:"resources"`
}
type Progress func(string) error

type Tool struct {
	Spec Spec
	// Callbacks are synchronous. Execute must honor context cancellation and
	// Progress errors and finish all work before returning.
	//
	// Prepare is read-only and required for every write, whether or not
	// RequiresApproval is set: it is the only source of the resources, target
	// versions and impact an operation is bound to, and is re-run after
	// authorization to detect a changed target.
	Prepare func(context.Context, ExecutionContext, Call) (Intent, error)
	Execute func(context.Context, ExecutionContext, Call, Progress) (Result, error)
}

type Authorizer interface {
	CheckTool(context.Context, ExecutionContext, Call) error
}
type AuthorizerFunc func(context.Context, ExecutionContext, Call) error

func (f AuthorizerFunc) CheckTool(ctx context.Context, ec ExecutionContext, c Call) error {
	return f(ctx, ec, c)
}

type Proposal struct {
	Principal   Principal
	RunID       string
	OperationID string
	Call        Call
	Intent      Intent
	Fingerprint string
}

type Decision string

const (
	Approved Decision = "approved"
	Pending  Decision = "pending"
	Rejected Decision = "rejected"
)

type Approval struct {
	Decision    Decision
	Fingerprint string
	ExpiresAt   time.Time
}

// ApprovalGate verifies/consumes a host approval bound to the exact proposal.
// Fingerprint matching is not signature verification: the host must authenticate
// the approving actor, enforce expiry/consumption and never trust a client boolean.
type ApprovalGate interface {
	Approve(context.Context, Proposal) (Approval, error)
}
type ApprovalFunc func(context.Context, Proposal) (Approval, error)

func (f ApprovalFunc) Approve(ctx context.Context, p Proposal) (Approval, error) { return f(ctx, p) }
