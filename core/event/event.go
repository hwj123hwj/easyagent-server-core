// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package event contains only public, host-filtered progress and reply events.
package event

import "github.com/hwj123hwj/easyagent-server-core/core/tool"

type Kind string

const (
	Delta            Kind = "delta"
	ToolStart        Kind = "tool.start"
	ToolProgress     Kind = "tool.progress"
	ToolEnd          Kind = "tool.end"
	ApprovalRequired Kind = "approval.required"
	Done             Kind = "done"
	Error            Kind = "error"
)

type ApprovalView struct {
	Tool        string          `json:"tool"`
	OperationID string          `json:"operation_id,omitempty"`
	Summary     string          `json:"summary"`
	Resources   []tool.Resource `json:"resources"`
	Fingerprint string          `json:"fingerprint"`
}

type Event struct {
	Kind     Kind          `json:"kind"`
	RunID    string        `json:"run_id"`
	Turn     int           `json:"turn"`
	CallID   string        `json:"call_id,omitempty"`
	Tool     string        `json:"tool,omitempty"`
	Text     string        `json:"text,omitempty"`
	Status   tool.Status   `json:"status,omitempty"`
	Code     string        `json:"code,omitempty"`
	Approval *ApprovalView `json:"approval,omitempty"`
}

// Handler runs synchronously. Returning an error stops subsequent model/tools.
// delta is a temporary preview identified by RunID/Turn, not a committed answer.
type Handler func(Event) error
