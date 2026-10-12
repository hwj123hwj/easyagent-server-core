// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package tool_test

import (
	"encoding/json"
	"testing"

	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func TestStrictArguments(t *testing.T) {
	s := tool.Schema{Type: "object", Properties: map[string]tool.Schema{
		"job_id": {Type: "string", Enum: []string{"job-1"}}, "count": {Type: "integer"},
		"tags": {Type: "array", MaxItems: 2, Items: &tool.Schema{Type: "string", MaxLength: 2}},
	}, Required: []string{"job_id"}}
	if err := s.Check(); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"job_id":"job-1"}`, `{"count":1e1,"job_id":"job-1","tags":["中文"]}`} {
		if _, err := s.Normalize([]byte(raw)); err != nil {
			t.Fatalf("valid input rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		`{}`, `{"job_id":"job-2"}`, `{"job_id":"job-1","command":"anything"}`,
		`{"job_id":"job-1","job_id":"job-2"}`, `{"job_id":"job-1"} {}`,
		`{"job_id":"job-1","count":1.1}`, `{"job_id":"job-1","count":1e1000000000}`,
		`{"job_id":"job-1","tags":["a","b","c"]}`, `{"job_id":"job-1","tags":["abc"]}`,
		`{"job_id":null}`, `{"job_id":"` + string([]byte{0xff}) + `"}`,
	} {
		if _, err := s.Normalize([]byte(raw)); err == nil {
			t.Errorf("bad input accepted: %q", raw)
		}
	}
	a, _ := s.Normalize([]byte(`{"count":2,"job_id":"job-1"}`))
	b, _ := s.Normalize([]byte(`{ "job_id":"job-1", "count":2 }`))
	if string(a) != string(b) {
		t.Fatal("unstable key ordering")
	}
	var schema map[string]any
	if err := json.Unmarshal(s.JSON(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("object not closed")
	}
}

func TestRejectInvalidSchema(t *testing.T) {
	for _, s := range []tool.Schema{{Type: "anything"}, {Type: "array"}, {Type: "boolean", MinLength: 1}, {Type: "object", Required: []string{"missing"}}, {Type: "string", MinLength: 4, MaxLength: 2}} {
		if s.Check() == nil {
			t.Errorf("accepted invalid schema %+v", s)
		}
	}
}
