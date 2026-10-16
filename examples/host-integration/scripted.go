// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
)

// fixtureModel is an OFFLINE protocol fixture. It does not parse or route real
// natural language; production supplies its own Provider and lets the model
// choose tools. Nothing in memoryHost recognizes pronouns/permission keywords.
func fixtureModel(change bool) model.Provider {
	turn := 0
	return model.ProviderFunc(func(_ context.Context, r model.Request, _ func(string) error) (model.Response, error) {
		turn++
		call := func(name, args string) (model.Response, error) {
			exposed := false
			for _, t := range r.Tools {
				if t.Name == name {
					exposed = true
				}
			}
			if !exposed {
				return model.Response{}, errors.New("fixture attempted an unexposed tool")
			}
			return model.Response{FinishReason: model.ToolUse, ToolCalls: []model.ToolCall{{ID: fmt.Sprintf("call-%d", turn), Name: name, Arguments: json.RawMessage(args)}}}, nil
		}
		if turn == 1 && len(r.Tools) != 1 {
			return model.Response{}, errors.New("eager tool schemas")
		}
		if change {
			switch turn {
			case 1:
				return call(runner.DiscoverTools, `{"groups":["system.members"]}`)
			case 2:
				return call("member.read", `{"member_id":"member-a"}`)
			case 3:
				return call("member.update", `{"member_id":"member-a","role":"analyst"}`)
			default:
				return model.Response{}, errors.New("expected pending approval")
			}
		}
		switch turn {
		case 1:
			return call(runner.DiscoverTools, `{"groups":["business.usage"]}`)
		case 2:
			return call("usage.daily", `{"member_id":"member-a"}`)
		case 3:
			return call(runner.DiscoverTools, `{"groups":["system.members"]}`)
		case 4:
			return call("member.read", `{"member_id":"member-a"}`)
		default:
			return model.Response{FinishReason: model.Stop, Text: "Fictional member-a used 15,000 tokens; known cost USD 0.18. The source is partial and no report is published. The member currently has role employee."}, nil
		}
	})
}
