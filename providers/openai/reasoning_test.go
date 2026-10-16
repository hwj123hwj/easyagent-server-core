// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package openai

import (
	"context"
	"encoding/json"
	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransientReasoningRoundTripWithoutPublicSerialization(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		var q map[string]any
		json.NewDecoder(r.Body).Decode(&q)
		if count == 2 {
			messages := q["messages"].([]any)
			a := messages[len(messages)-2].(map[string]any)
			if a["reasoning_content"] != "private-state" {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":{"message":"Missing reasoning_content field secret:xxx"}}`))
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"private-state\",\"tool_calls\":[{\"index\":0,\"id\":\"c\",\"type\":\"function\",\"function\":{\"name\":\"probe\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"))
		} else {
			w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
		}
	}))
	defer server.Close()
	p, e := New(Config{Endpoint: server.URL, APIKey: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	req := model.Request{Model: "strict", Messages: []model.Message{{Role: model.User, Text: "probe"}}, Tools: []model.ToolDefinition{{Name: "probe", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}, MaxOutputTokens: 100, MaxResponseBytes: 4096}
	a, e := p.Generate(context.Background(), req, func(s string) error {
		if strings.Contains(s, "private") {
			t.Fatal("reasoning emitted")
		}
		return nil
	})
	if e != nil || a.ReasoningContent != "private-state" {
		t.Fatal(a, e)
	}
	req.Messages = append(req.Messages, model.Message{Role: model.Assistant, ReasoningContent: a.ReasoningContent, ToolCalls: a.ToolCalls}, model.Message{Role: model.Tool, Text: `{"ok":true}`, ToolCallID: "c"})
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "private-state") {
		t.Fatal("reasoning publicly serialized")
	}
	a, e = p.Generate(context.Background(), req, nil)
	if e != nil || a.Text != "ok" {
		t.Fatal(a, e)
	}
}
