// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
	"github.com/hwj123hwj/easyagent-server-core/providers/openai"
)

func request() model.Request {
	return model.Request{Model: "fixture-model", System: "host instructions", Messages: []model.Message{{Role: model.User, Text: "inspect"}}, MaxOutputTokens: 100, MaxResponseBytes: 64 << 10}
}
func server(t *testing.T, body string) *openai.Provider {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	p, err := openai.New(openai.Config{Endpoint: s.URL + "/router/chat/completions"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func frame(value string) string { return "data: " + value + "\n\n" }

func TestFixedEndpointStreamingAndToolAssembly(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/custom/router/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" || r.Header.Get("X-Router-Profile") != "fixture" {
			t.Error("request configuration changed")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "fixture-model" || body["stream"] != true || body["max_tokens"] != float64(100) {
			t.Error("bad request")
		}
		tools := body["tools"].([]any)
		fn := tools[0].(map[string]any)["function"].(map[string]any)
		if fn["name"] != "job_runs" {
			t.Error("wire alias missing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Deliberately split every byte, including UTF-8 and JSON/tool arguments.
		bodyText := frame(`{"choices":[{"index":0,"delta":{"content":"正在查","reasoning_content":"private reasoning"}}]}`) +
			frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"job_","arguments":"{\"id\":"}}]}}]}`) +
			frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"runs","arguments":"\"job-1\"}"}}]},"finish_reason":"tool_calls"}]}`) +
			frame(`{"choices":[],"usage":{"prompt_tokens":1}}`) + frame("[DONE]")
		for _, b := range []byte(bodyText) {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}))
	defer s.Close()
	p, err := openai.New(openai.Config{Endpoint: s.URL + "/custom/router/chat/completions", APIKey: "fixture-key", Headers: http.Header{"X-Router-Profile": []string{"fixture"}}})
	if err != nil {
		t.Fatal(err)
	}
	r := request()
	r.Tools = []model.ToolDefinition{{Name: "job.runs", Version: "1", Description: "inspect job", Parameters: json.RawMessage(`{"type":"object"}`)}}
	var preview strings.Builder
	out, err := p.Generate(context.Background(), r, func(s string) error { preview.WriteString(s); return nil })
	if err != nil || out.Text != "正在查" || preview.String() != out.Text || out.FinishReason != model.ToolUse || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "job.runs" || string(out.ToolCalls[0].Arguments) != `{"id":"job-1"}` || requests.Load() != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, requests.Load())
	}
}

func TestIncompleteInvalidAndOversizedStreams(t *testing.T) {
	text := `{"choices":[{"index":0,"delta":{"content":"preview"}}]}`
	stop := `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	for _, tc := range []struct{ name, body string }{
		{"EOF before finish", frame(text)},
		{"EOF before DONE", frame(text) + frame(stop)},
		{"DONE before finish", frame(text) + frame("[DONE]")},
		{"length", frame(`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`) + frame("[DONE]")},
		{"malformed", frame("{broken}")},
		{"error body", frame(`{"error":{"message":"private upstream body"}}`)},
		{"tool reason without calls", frame(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`) + frame("[DONE]")},
		{"invalid UTF8", frame(`{"choices":[{"index":0,"delta":{"content":"`+string([]byte{0xff})+`"}}]}`) + frame(stop) + frame("[DONE]")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server(t, tc.body).Generate(context.Background(), request(), nil)
			if err == nil {
				t.Fatal("accepted incomplete stream")
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatal("raw error leaked")
			}
		})
	}
	r := request()
	r.MaxResponseBytes = 100
	_, err := server(t, frame(`{"choices":[{"index":0,"delta":{"content":"`+strings.Repeat("x", 2000)+`"}}]}`)).Generate(context.Background(), r, nil)
	if err == nil {
		t.Fatal("response byte limit ignored")
	}
}

func TestTextMultilineAndCallbackError(t *testing.T) {
	body := "event: message\ndata: {\"choices\":\ndata: [{\"index\":0,\"delta\":{\"content\":\"你好\"},\"finish_reason\":\"stop\"}]}\n\n" + frame("[DONE]")
	p := server(t, body)
	out, err := p.Generate(context.Background(), request(), nil)
	if err != nil || out.Text != "你好" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	want := errors.New("consumer disconnected")
	_, err = p.Generate(context.Background(), request(), func(string) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("callback error lost: %v", err)
	}
}

func TestStatusRedirectCancellationAndNoRetries(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/error":
			w.WriteHeader(500)
			fmt.Fprint(w, "secret upstream body")
		case "/redirect":
			http.Redirect(w, r, target.URL, 302)
		case "/cancel":
			w.Header().Set("Content-Type", "text/event-stream")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		}
	}))
	defer s.Close()
	for _, path := range []string{"/error", "/redirect"} {
		p, err := openai.New(openai.Config{Endpoint: s.URL + path, Client: s.Client()})
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.Generate(context.Background(), request(), nil)
		var status *openai.HTTPError
		if !errors.As(err, &status) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("bad error: %v", err)
		}
	}
	p, _ := openai.New(openai.Config{Endpoint: s.URL + "/cancel"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := p.Generate(ctx, request(), nil); finished <- err }()
	select {
	case <-started:
		cancel()
	case <-ctx.Done():
		t.Fatal("request did not start")
	}
	err := <-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	if requests.Load() != 3 {
		t.Fatalf("unexpected retries: %d", requests.Load())
	}
}

func TestAliasCollisionBeforeHTTP(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("network called with ambiguous tools") }))
	defer s.Close()
	p, _ := openai.New(openai.Config{Endpoint: s.URL})
	r := request()
	r.Tools = []model.ToolDefinition{{Name: "job.runs"}, {Name: "job_runs"}}
	_, err := p.Generate(context.Background(), r, nil)
	if !errors.Is(err, openai.ErrConfiguration) {
		t.Fatal(err)
	}
}

func TestRejectUntrustedEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "file:///tmp/test", "https://user:pass@example.invalid", "http://example.invalid/chat", "https://example.invalid/chat#fragment"} {
		if _, err := openai.New(openai.Config{Endpoint: endpoint}); err == nil {
			t.Fatalf("accepted %q", endpoint)
		}
	}
}

func TestWireAndDecodedBudgetsAreIndependent(t *testing.T) {
	// Router may emit reasoning and one-character content frames. Their JSON
	// envelope must not consume the caller's decoded answer budget.
	body := frame(`{"choices":[{"index":0,"delta":{"reasoning_content":"` + strings.Repeat("private reasoning ", 500) + `"}}]}`)
	for range 200 {
		body += frame(`{"choices":[{"index":0,"delta":{"content":"字"}}]}`)
	}
	body += frame(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) + frame("[DONE]")
	r := request()
	r.MaxResponseBytes = 2048
	var preview strings.Builder
	out, err := server(t, body).Generate(context.Background(), r, func(s string) error { preview.WriteString(s); return nil })
	if err != nil || out.Text != strings.Repeat("字", 200) || preview.String() != out.Text {
		t.Fatalf("framing consumed answer budget: bytes=%d err=%v", len(out.Text), err)
	}
	r.MaxResponseBytes = 100
	preview.Reset()
	_, err = server(t, body).Generate(context.Background(), r, func(s string) error { preview.WriteString(s); return nil })
	if !errors.Is(err, openai.ErrLimit) || preview.Len() > r.MaxResponseBytes {
		t.Fatalf("decoded streaming budget ignored: bytes=%d err=%v", preview.Len(), err)
	}
}

func TestWireBudgetAndToolArgumentsRemainBounded(t *testing.T) {
	body := frame(`{"choices":[{"index":0,"delta":{"reasoning_content":"`+strings.Repeat("x", 2000)+`"}}]}`) +
		frame(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`) + frame("[DONE]")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
	defer s.Close()
	p, err := openai.New(openai.Config{Endpoint: s.URL, MaxResponseBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Generate(context.Background(), request(), nil); !errors.Is(err, openai.ErrLimit) {
		t.Fatalf("wire limit ignored: %v", err)
	}
	body = frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"runs","arguments":"{\"id\":\"`+strings.Repeat("x", 500)+`\"}"}}]},"finish_reason":"tool_calls"}]}`) + frame("[DONE]")
	r := request()
	r.MaxResponseBytes = 100
	r.Tools = []model.ToolDefinition{{Name: "runs", Version: "1", Parameters: json.RawMessage(`{"type":"object"}`)}}
	if _, err = server(t, body).Generate(context.Background(), r, nil); !errors.Is(err, openai.ErrLimit) {
		t.Fatalf("tool argument budget ignored: %v", err)
	}
}

func TestDeferredHistoricalToolsRemainHistoryOnly(t *testing.T) {
	for _, kind := range []string{"history", "collision", "invalid", "hidden response"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
					Messages []struct {
						Calls []struct {
							Function struct {
								Name string `json:"name"`
							} `json:"function"`
						} `json:"tool_calls"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				historicalName := ""
				for _, m := range body.Messages {
					for _, c := range m.Calls {
						historicalName = c.Function.Name
					}
				}
				if len(body.Tools) != 1 || body.Tools[0].Function.Name != "discover_tools" || historicalName != "usage_daily" {
					t.Error("deferred schema exposed or history lost")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if kind == "hidden response" {
					fmt.Fprint(w, frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"forged","type":"function","function":{"name":"usage_daily","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)+frame("[DONE]"))
					return
				}
				fmt.Fprint(w, frame(`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`)+frame("[DONE]"))
			}))
			defer s.Close()
			p, e := openai.New(openai.Config{Endpoint: s.URL})
			if e != nil {
				t.Fatal(e)
			}
			r := request()
			r.Tools = []model.ToolDefinition{{Name: "discover_tools", Version: "1", Parameters: json.RawMessage(`{"type":"object"}`)}}
			name := "usage.daily"
			if kind == "collision" {
				r.Tools = append(r.Tools, model.ToolDefinition{Name: "usage_daily", Version: "1", Parameters: json.RawMessage(`{"type":"object"}`)})
			}
			if kind == "invalid" {
				name = "invalid name"
			}
			r.Messages = append(r.Messages, model.Message{Role: model.Assistant, ToolCalls: []model.ToolCall{{ID: "previous", Name: name, Arguments: json.RawMessage(`{}`)}}}, model.Message{Role: model.Tool, ToolCallID: "previous", Text: "fictional usage result"}, model.Message{Role: model.User, Text: "continue"})
			out, err := p.Generate(context.Background(), r, func(string) error { return nil })
			e = err
			if kind == "hidden response" {
				if e != nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != "usage.daily" {
					t.Fatalf("hidden call identity: %+v %v", out, e)
				}
			} else if kind == "history" {
				if e != nil || calls.Load() != 1 {
					t.Fatal(e)
				}
			} else if !errors.Is(e, openai.ErrProtocol) {
				t.Fatalf("%s err=%v", kind, e)
			}
			if (kind == "collision" || kind == "invalid") && calls.Load() != 0 {
				t.Fatal("invalid history reached network")
			}
		})
	}
}

func TestUnknownSafeToolNameIsNotAProtocolFailure(t *testing.T) {
	for _, name := range []string{"company_usage_ranking", "unsafe name"} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "guess", "type": "function", "function": map[string]string{"name": name, "arguments": "{}"}}}}, "finish_reason": "tool_calls"}}})
			p := server(t, frame(string(raw))+frame("[DONE]"))
			out, err := p.Generate(t.Context(), request(), func(string) error { return nil })
			if name == "unsafe name" {
				if !errors.Is(err, openai.ErrProtocol) {
					t.Fatal(err)
				}
				return
			}
			if err != nil || len(out.ToolCalls) != 1 || out.ToolCalls[0].Name != name {
				t.Fatalf("response=%+v err=%v", out, err)
			}
		})
	}
}

func TestSSEUnknownToolFeedbackThenActualAuthorizedRead(t *testing.T) {
	turns, executed := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role, Content string
				ToolCallID    string `json:"tool_call_id"`
			}
			Tools []struct{ Function struct{ Name string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Tools) != 1 || body.Tools[0].Function.Name != "usage_daily" {
			t.Error("guessed tool was exposed")
		}
		turns++
		w.Header().Set("Content-Type", "text/event-stream")
		if turns == 1 {
			fmt.Fprint(w, frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"guess","type":"function","function":{"name":"company_usage_ranking","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)+frame("[DONE]"))
			return
		}
		last := body.Messages[len(body.Messages)-1]
		if turns == 2 {
			if last.Role != "tool" || last.ToolCallID != "guess" || !strings.Contains(last.Content, "tool_unavailable") {
				t.Error("failure not fed back")
			}
			fmt.Fprint(w, frame(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"actual","type":"function","function":{"name":"usage_daily","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)+frame("[DONE]"))
			return
		}
		if last.ToolCallID != "actual" || !strings.Contains(last.Content, "fictional usage") {
			t.Error("missing actual result")
		}
		fmt.Fprint(w, frame(`{"choices":[{"index":0,"delta":{"content":"Checked available usage."},"finish_reason":"stop"}]}`)+frame("[DONE]"))
	}))
	defer srv.Close()
	p, err := openai.New(openai.Config{Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	tt := tool.Tool{Spec: tool.Spec{Name: "usage.daily", Version: "1", Description: "fictional usage", Effect: tool.Read, Input: tool.Schema{Type: "object"}, Timeout: time.Second, MaxResultBytes: 1024}, Execute: func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		executed++
		return tool.Result{Status: tool.Available, Summary: "fictional usage"}, nil
	}}
	core, err := runner.New(runner.Definition{ID: "test", Version: "1", Model: "test", Tools: []tool.Tool{tt}}, runner.Dependencies{Provider: p, Authorizer: tool.AuthorizerFunc(func(context.Context, tool.ExecutionContext, tool.Call) error { return nil })})
	if err != nil {
		t.Fatal(err)
	}
	out, err := core.Run(t.Context(), runner.Request{Principal: tool.Principal{TenantID: "test", SubjectID: "test"}, RunID: "sse-test", Question: "inspect"}, nil)
	if err != nil || out.State != runner.Completed || turns != 3 || executed != 1 || out.ToolCalls != 2 {
		t.Fatalf("state=%s err=%v turns=%d executed=%d tools=%d", out.State, err, turns, executed, out.ToolCalls)
	}
}
