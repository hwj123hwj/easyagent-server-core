// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"testing"

	"github.com/hwj123hwj/easyagent-server-core/core/event"
	"github.com/hwj123hwj/easyagent-server-core/core/runner"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

func TestOfflineScenarios(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   runner.State
		effects int
	}{
		{"query", runner.Completed, 0}, {"history", runner.Completed, 0}, {"diagnostics", runner.Completed, 0},
		{"pending", runner.PendingApproval, 0}, {"approved", runner.Completed, 1}, {"denied", runner.Interrupted, 0},
		{"unknown", runner.Interrupted, 1}, {"partial", runner.Interrupted, 1}, {"duplicate", runner.Completed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []event.Event
			r, err := runScenario(context.Background(), tc.name, nil, func(e event.Event) error { events = append(events, e); return nil })
			if err != nil || r.Outcome.State != tc.state || r.SideEffects != tc.effects {
				t.Fatalf("report=%+v err=%v", r, err)
			}
			done := false
			for _, e := range events {
				if e.Kind == event.Done {
					done = true
				}
				if e.RunID == "" || e.Turn < 1 {
					t.Fatalf("unidentified event: %+v", e)
				}
			}
			if (tc.state == runner.Completed) != done {
				t.Fatalf("done=%v state=%s", done, tc.state)
			}
			if tc.name == "unknown" && r.Outcome.Observations[len(r.Outcome.Observations)-1].Result.Status != tool.Unknown {
				t.Fatal("unknown side effect was lost")
			}
		})
	}
}
