// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package runner

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hwj123hwj/easyagent-server-core/core/model"
	"github.com/hwj123hwj/easyagent-server-core/core/tool"
)

// DiscoverTools is reserved only when Definition.Discovery is configured.
const DiscoverTools = "discover_tools"

// ToolGroup is a compact, host-authorized capability directory. Tools contains
// names already registered in Definition.Tools, never code or remote resources.
type ToolGroup struct {
	ID          string
	Description string
	Tools       []string
}

// Discovery enables opt-in progressive schema exposure. Ungrouped tools remain
// visible. Each discovery REPLACES the selected groups for the next model turn;
// it never grows a session-wide tool set or changes authorization. The cap
// includes discover_tools and ungrouped tools. Hosts must filter groups and
// registrations by authenticated authority before New, and reauthorize calls.
type Discovery struct {
	Groups         []ToolGroup
	MaxActiveTools int
}

func prepareDiscovery(d *Definition) error {
	if d.Discovery == nil {
		return nil
	}
	cfg := *d.Discovery
	if len(cfg.Groups) == 0 || len(cfg.Groups) > 64 || cfg.MaxActiveTools < 1 {
		return ErrConfiguration
	}
	names := map[string]bool{}
	for _, t := range d.Tools {
		if t.Spec.Name == DiscoverTools {
			return ErrConfiguration
		}
		names[t.Spec.Name] = true
	}
	assigned := map[string]bool{}
	ids := map[string]bool{}
	cfg.Groups = append([]ToolGroup(nil), cfg.Groups...)
	var description strings.Builder
	description.WriteString("Discover and load registered tool groups for the NEXT model turn. This replaces previously selected groups; history is retained. Select only groups needed now. An empty list unloads all groups. Authorization and approval still apply. Available groups:\n")
	enums := []string{}
	for i, g := range cfg.Groups {
		if !namePattern.MatchString(g.ID) || ids[g.ID] || !utf8.ValidString(g.Description) || len(g.Description) == 0 || len(g.Description) > 1024 || len(g.Tools) == 0 {
			return ErrConfiguration
		}
		ids[g.ID] = true
		enums = append(enums, g.ID)
		cfg.Groups[i].Tools = append([]string(nil), g.Tools...)
		for _, name := range g.Tools {
			if !names[name] || assigned[name] {
				return ErrConfiguration
			}
			assigned[name] = true
		}
		description.WriteString(g.ID + ": " + g.Description + "\n")
	}
	always := len(d.Tools) - len(assigned) + 1
	if always > cfg.MaxActiveTools {
		return ErrConfiguration
	}
	for _, g := range cfg.Groups {
		if always+len(g.Tools) > cfg.MaxActiveTools {
			return ErrConfiguration
		}
	}
	d.Discovery = &cfg
	d.Tools = append(append([]tool.Tool(nil), d.Tools...), tool.Tool{Spec: tool.Spec{Name: DiscoverTools, Version: "1", Description: description.String(), Effect: tool.Read, Input: tool.Schema{Type: "object", Properties: map[string]tool.Schema{"groups": {Type: "array", MaxItems: len(cfg.Groups), Items: &tool.Schema{Type: "string", Enum: enums}}}, Required: []string{"groups"}}, Timeout: 5 * time.Second, MaxResultBytes: 8192}, Execute: func(context.Context, tool.ExecutionContext, tool.Call, tool.Progress) (tool.Result, error) {
		return tool.Result{Status: tool.Invalid, Summary: "Discovery is only available inside Run."}, nil
	}})
	return nil
}

type exposure struct {
	active  map[string]bool
	grouped map[string]bool
	config  *Discovery
}

func (r *Runner) exposure() *exposure {
	e := &exposure{active: map[string]bool{}, grouped: map[string]bool{}, config: r.definition.Discovery}
	if e.config != nil {
		for _, g := range e.config.Groups {
			for _, n := range g.Tools {
				e.grouped[n] = true
			}
		}
	}
	for n := range r.tools {
		if !e.grouped[n] {
			e.active[n] = true
		}
	}
	return e
}
func (e *exposure) definitions(tools []tool.Tool) []model.ToolDefinition {
	out := []model.ToolDefinition{}
	for _, t := range tools {
		if e.active[t.Spec.Name] {
			out = append(out, model.ToolDefinition{Name: t.Spec.Name, Version: t.Spec.Version, Description: t.Spec.Description, Parameters: t.Spec.Input.JSON()})
		}
	}
	return out
}
func (e *exposure) load(_ context.Context, _ tool.ExecutionContext, c tool.Call, _ tool.Progress) (tool.Result, error) {
	var args struct {
		Groups []string `json:"groups"`
	}
	if json.Unmarshal(c.Arguments, &args) != nil {
		return tool.Result{}, tool.ErrArguments
	}
	next := map[string]bool{}
	for n := range e.active {
		if !e.grouped[n] {
			next[n] = true
		}
	}
	seen := map[string]bool{}
	for _, id := range args.Groups {
		if seen[id] {
			return tool.Result{Status: tool.Invalid, Summary: "Select each group only once; active tools unchanged."}, nil
		}
		seen[id] = true
		found := false
		for _, g := range e.config.Groups {
			if g.ID == id {
				found = true
				for _, n := range g.Tools {
					next[n] = true
				}
				break
			}
		}
		if !found {
			return tool.Result{Status: tool.Invalid, Summary: "Group unavailable; active tools unchanged."}, nil
		}
	}
	if len(next) > e.config.MaxActiveTools {
		return tool.Result{Status: tool.Invalid, Summary: "Too many active tools; select fewer groups. Active tools unchanged."}, nil
	}
	e.active = next
	raw, _ := json.Marshal(args.Groups)
	return tool.Result{Status: tool.Available, Summary: "Selected groups for the next model turn: " + string(raw) + ". Call their structured tools next; no business query or configuration change has been executed."}, nil
}
