// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package openai supplies an optional, fixed-endpoint Chat Completions HTTP
// provider. Its message/SSE protocol is adapted from EasyAgent; see NOTICE.md.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
)

var (
	ErrConfiguration = errors.New("invalid HTTP provider configuration")
	ErrProtocol      = errors.New("invalid or incomplete model stream")
	ErrLimit         = errors.New("model HTTP byte limit exceeded")
)

type Config struct {
	// Endpoint is the exact trusted URL, including its chat/completions path.
	// No /v1 suffix is added. Redirects are refused, including with a host Client.
	Endpoint         string
	APIKey           string
	Headers          http.Header
	Client           *http.Client
	AllowHTTP        bool // Explicit host choice; loopback test servers work without it.
	MaxRequestBytes  int
	MaxResponseBytes int
}

type Provider struct {
	endpoint                string
	key                     string
	headers                 http.Header
	client                  *http.Client
	maxRequest, maxResponse int
}

func New(c Config) (*Provider, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, ErrConfiguration
	}
	if u.Scheme == "http" && !c.AllowHTTP {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return nil, ErrConfiguration
		}
	}
	if c.MaxRequestBytes == 0 {
		c.MaxRequestBytes = 1 << 20
	}
	if c.MaxResponseBytes == 0 {
		c.MaxResponseBytes = 1 << 20
	}
	if c.MaxRequestBytes < 1 || c.MaxResponseBytes < 1 {
		return nil, ErrConfiguration
	}
	client := http.Client{Timeout: 90 * time.Second}
	if c.Client != nil {
		client = *c.Client
	} else {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 30 * time.Second
		client.Transport = transport
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Provider{endpoint: u.String(), key: c.APIKey, headers: c.Headers.Clone(), client: &client, maxRequest: c.MaxRequestBytes, maxResponse: c.MaxResponseBytes}, nil
}

type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("model HTTP status %d", e.StatusCode) }

type functionCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}
type function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type wireMessage struct {
	Role    string         `json:"role"`
	Content string         `json:"content"`
	Calls   []functionCall `json:"tool_calls,omitempty"`
	CallID  string         `json:"tool_call_id,omitempty"`
}
type wireTool struct {
	Type     string         `json:"type"`
	Function wireDefinition `json:"function"`
}
type wireDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}
type wireRequest struct {
	Model     string        `json:"model"`
	Messages  []wireMessage `json:"messages"`
	Tools     []wireTool    `json:"tools,omitempty"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens"`
}

func (p *Provider) Generate(ctx context.Context, req model.Request, emit func(string) error) (model.Response, error) {
	if req.Model == "" || req.MaxOutputTokens < 1 {
		return model.Response{}, ErrConfiguration
	}
	aliases := map[string]string{}
	names := map[string]string{}
	wire := wireRequest{Model: req.Model, Stream: true, MaxTokens: req.MaxOutputTokens}
	if req.System != "" {
		wire.Messages = append(wire.Messages, wireMessage{Role: "system", Content: req.System})
	}
	for _, t := range req.Tools {
		// Generic tool names may contain dots; the compatible HTTP protocol uses
		// underscores. Reject collisions before any network request.
		alias := strings.ReplaceAll(t.Name, ".", "_")
		if _, exists := aliases[alias]; exists {
			return model.Response{}, ErrConfiguration
		}
		aliases[alias] = t.Name
		names[t.Name] = alias
		wire.Tools = append(wire.Tools, wireTool{Type: "function", Function: wireDefinition{Name: alias, Description: t.Description, Parameters: t.Parameters}})
	}
	for _, m := range req.Messages {
		if m.Role != model.User && m.Role != model.Assistant && m.Role != model.Tool {
			return model.Response{}, ErrProtocol
		}
		w := wireMessage{Role: string(m.Role), Content: m.Text, CallID: m.ToolCallID}
		for _, c := range m.ToolCalls {
			name, ok := names[c.Name]
			if !ok {
				return model.Response{}, ErrProtocol
			}
			w.Calls = append(w.Calls, functionCall{ID: c.ID, Type: "function", Function: function{Name: name, Arguments: string(c.Arguments)}})
		}
		wire.Messages = append(wire.Messages, w)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return model.Response{}, ErrProtocol
	}
	if len(body) > p.maxRequest {
		return model.Response{}, ErrLimit
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return model.Response{}, ErrConfiguration
	}
	httpReq.Header = p.headers.Clone()
	if httpReq.Header == nil {
		httpReq.Header = make(http.Header)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if p.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.key)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return model.Response{}, ctx.Err()
		}
		return model.Response{}, errors.New("model HTTP transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return model.Response{}, &HTTPError{StatusCode: resp.StatusCode}
	}
	maxBytes := p.maxResponse
	if req.MaxResponseBytes > 0 && req.MaxResponseBytes < maxBytes {
		maxBytes = req.MaxResponseBytes
	}
	return parseSSE(ctx, resp.Body, maxBytes, aliases, emit)
}

type chunk struct {
	Choices []choice        `json:"choices"`
	Error   json.RawMessage `json:"error"`
}
type choice struct {
	Index        int     `json:"index"`
	FinishReason *string `json:"finish_reason"`
	Delta        delta   `json:"delta"`
}
type delta struct {
	Content string      `json:"content"`
	Tools   []toolDelta `json:"tool_calls"`
}
type toolDelta struct {
	Index    int      `json:"index"`
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function function `json:"function"`
}
type accumulated struct {
	id, name string
	args     strings.Builder
}

func parseSSE(ctx context.Context, body io.Reader, max int, aliases map[string]string, emit func(string) error) (model.Response, error) {
	limited := &io.LimitedReader{R: body, N: int64(max) + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, min(4096, max)), max+1)
	var text strings.Builder
	var data []string
	calls := map[int]*accumulated{}
	reason := ""
	done := false
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			if reason == "" {
				return ErrProtocol
			}
			done = true
			return nil
		}
		if !utf8.ValidString(payload) {
			return ErrProtocol
		}
		var c chunk
		if json.Unmarshal([]byte(payload), &c) != nil || (len(c.Error) > 0 && string(c.Error) != "null") {
			return ErrProtocol
		}
		if len(c.Choices) == 0 {
			return nil
		} // Usage-only chunk.
		if len(c.Choices) != 1 || c.Choices[0].Index != 0 || reason != "" {
			return ErrProtocol
		}
		ch := c.Choices[0]
		if ch.Delta.Content != "" {
			text.WriteString(ch.Delta.Content)
			if emit != nil {
				if err := emit(ch.Delta.Content); err != nil {
					return err
				}
			}
		}
		for _, part := range ch.Delta.Tools {
			if part.Index < 0 || part.Index > 1024 || (part.Type != "" && part.Type != "function") {
				return ErrProtocol
			}
			a := calls[part.Index]
			if a == nil {
				a = &accumulated{}
				calls[part.Index] = a
			}
			a.id += part.ID
			a.name += part.Function.Name
			a.args.WriteString(part.Function.Arguments)
		}
		if ch.FinishReason != nil {
			reason = *ch.FinishReason
			if reason != string(model.Stop) && reason != string(model.ToolUse) {
				return ErrProtocol
			}
		}
		return nil
	}
	for scanner.Scan() {
		if ctx.Err() != nil {
			return model.Response{}, ctx.Err()
		}
		if limited.N <= 0 {
			return model.Response{}, ErrLimit
		}
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return model.Response{}, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return model.Response{}, ctx.Err()
	}
	if limited.N <= 0 {
		return model.Response{}, ErrLimit
	}
	if scanner.Err() != nil {
		return model.Response{}, ErrProtocol
	}
	if !done && len(data) > 0 {
		if err := consume(); err != nil {
			return model.Response{}, err
		}
	}
	if !done || (reason == string(model.ToolUse)) != (len(calls) > 0) {
		return model.Response{}, ErrProtocol
	}
	response := model.Response{Text: text.String(), FinishReason: model.FinishReason(reason)}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	ids := map[string]bool{}
	for i, index := range indices {
		a := calls[index]
		if index != i || a.id == "" || a.name == "" || ids[a.id] || !json.Valid([]byte(a.args.String())) {
			return model.Response{}, ErrProtocol
		}
		ids[a.id] = true
		name := a.name
		if actual, ok := aliases[name]; ok {
			name = actual
		}
		response.ToolCalls = append(response.ToolCalls, model.ToolCall{ID: a.id, Name: name, Arguments: json.RawMessage(a.args.String())})
	}
	return response, nil
}
