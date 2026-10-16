// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package model defines the small provider boundary adapted from EasyAgent's
// sdk/ai protocols. See NOTICE.md for source and license information.
package model

import (
	"context"
	"encoding/json"
)

type Role string

const (
	User      Role = "user"
	Assistant Role = "assistant"
	Tool      Role = "tool"
)

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type Message struct {
	// ReasoningContent is opaque provider continuation state for this in-memory
	// tool loop only. Never persist it or include it in user-visible logs/events.
	ReasoningContent string     `json:"-"` // transient provider continuation only; never public/persisted
	Role             Role       `json:"role"`
	Text             string     `json:"text,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type ToolDefinition struct {
	Name        string
	Version     string
	Description string
	Parameters  json.RawMessage
}

type Request struct {
	Model            string
	System           string
	Messages         []Message
	Tools            []ToolDefinition
	MaxOutputTokens  int
	MaxResponseBytes int
}

type FinishReason string

const (
	Stop    FinishReason = "stop"
	ToolUse FinishReason = "tool_calls"
)

type Response struct {
	ReasoningContent string `json:"-"`
	Text             string
	ToolCalls        []ToolCall
	FinishReason     FinishReason
}

// Provider returns only a complete response. emit contains text increments,
// never private reasoning or raw tool arguments. It must run synchronously;
// no callback may occur after Generate returns. Respect context and emit errors.
// A host with an existing Router client can implement this interface directly.
type Provider interface {
	Generate(context.Context, Request, func(string) error) (Response, error)
}

type ProviderFunc func(context.Context, Request, func(string) error) (Response, error)

func (f ProviderFunc) Generate(ctx context.Context, r Request, emit func(string) error) (Response, error) {
	return f(ctx, r, emit)
}
