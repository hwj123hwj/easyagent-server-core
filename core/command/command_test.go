// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT
package command_test

import (
	"github.com/hwj123hwj/easyagent-server-core/core/command"
	"strings"
	"testing"
)

func TestParseControlCommands(t *testing.T) {
	for _, tc := range []struct {
		text  string
		kind  command.Kind
		found bool
	}{
		{" /NEW \t", command.New, true}, {"/reset", command.New, true}, {"/stop", command.Stop, true}, {"/cancel", command.Stop, true}, {"/status", command.Status, true}, {"/help", command.Help, true},
		{"/new title", command.Invalid, true}, {"/stop\nforce", command.Invalid, true}, {"/help;delete", command.Unknown, true}, {"/shell rm", command.Unknown, true}, {"/", command.Unknown, true}, {"/" + strings.Repeat("a", 9000), command.Invalid, true}, {"/new\x00", command.Invalid, true},
		{"解释 /new", command.None, false}, {"https://example.test/new", command.None, false}, {"`/new`", command.None, false}, {"", command.None, false},
	} {
		t.Run(tc.text[:min(30, len(tc.text))], func(t *testing.T) {
			c, found := command.Parse(tc.text)
			if found != tc.found || c.Kind != tc.kind {
				t.Fatalf("got %#v %v", c, found)
			}
		})
	}
}
func TestDefinitionsAreDetached(t *testing.T) {
	a := command.Definitions()
	if len(a) != 6 {
		t.Fatal(a)
	}
	a[0].Name = "changed"
	if command.Definitions()[0].Name == "changed" {
		t.Fatal("global definitions mutated")
	}
}
