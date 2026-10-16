// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Run without credentials, network access or a database. See README.md for the
// host responsibilities intentionally NOT implemented by this teaching example.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/hwj123hwj/easyagent-server-core/core/runner"
)

func main() {
	scenario := flag.String("scenario", "confirmed", "confirmed, pending, cancelled, revoked, changed, reset, duplicate, unknown")
	flag.Parse()
	if e := runExample(context.Background(), *scenario, os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func runExample(ctx context.Context, scenario string, w io.Writer) error {
	switch scenario {
	case "confirmed", "pending", "cancelled", "revoked", "changed", "reset", "duplicate", "unknown":
	default:
		return errors.New("unknown example scenario")
	}
	h := newMemoryHost()
	enc := json.NewEncoder(w)
	publish := func(phase string, out runner.Outcome, id string, err error) error {
		status := ""
		if err != nil {
			status = "blocked"
		}
		if errors.Is(err, runner.ErrUncertain) {
			status = "unknown"
		}
		return enc.Encode(struct {
			Phase      string       `json:"phase"`
			State      runner.State `json:"state"`
			ApprovalID string       `json:"approval_id,omitempty"`
			Answer     string       `json:"answer,omitempty"`
			Status     string       `json:"operation_status,omitempty"`
			Writes     int          `json:"business_writes"`
			ModelCalls int          `json:"model_calls"`
		}{phase, out.State, id, out.Text, status, h.Writes, out.ModelCalls})
	}
	first, e := h.message(ctx, "operator-demo", "direct-demo", "Inspect daily usage and current permissions for member-a.", fixtureModel(false))
	if e != nil {
		return e
	}
	if e = publish("cross-domain query", first.Outcome, "", nil); e != nil {
		return e
	}
	next, e := h.message(ctx, "operator-demo", "direct-demo", "Make them an analyst; show me the change first.", fixtureModel(true))
	if e != nil {
		return e
	}
	if next.ApprovalID == "" || h.Writes != 0 {
		return errors.New("preview executed or missing")
	}
	if e = publish("follow-up preview", next.Outcome, next.ApprovalID, nil); e != nil {
		return e
	}
	if scenario == "pending" {
		return nil
	}
	switch scenario {
	case "revoked":
		a := h.Accounts["operator-demo"]
		a.Manage = false
		a.Revision++
		h.Accounts["operator-demo"] = a
	case "changed":
		m := h.Members["member-a"]
		m.Revision++
		h.Members[m.ID] = m
	case "reset":
		if e = h.reset("operator-demo", "direct-demo"); e != nil {
			return e
		}
	case "unknown":
		h.LoseAcknowledgement = true
	}
	out, e := h.decide(ctx, "operator-demo", "direct-demo", next.ApprovalID, scenario != "cancelled")
	if (scenario == "revoked" || scenario == "reset") && !errors.Is(e, errSession) {
		return errors.New("expected authority/session rejection")
	}
	if scenario == "changed" && !errors.Is(e, runner.ErrApproval) {
		return errors.New("expected stale target rejection")
	}
	if scenario == "unknown" && !errors.Is(e, runner.ErrUncertain) {
		return errors.New("expected unconfirmed operation")
	}
	if (scenario == "confirmed" || scenario == "duplicate" || scenario == "cancelled") && e != nil {
		return e
	}
	if e = publish("decision", out, next.ApprovalID, e); e != nil {
		return e
	}
	if scenario == "duplicate" {
		out, e = h.decide(ctx, "operator-demo", "direct-demo", next.ApprovalID, true)
		if e != nil || h.Writes != 1 {
			return errors.New("duplicate decision was not idempotent")
		}
		return publish("duplicate decision", out, next.ApprovalID, e)
	}
	return nil
}
