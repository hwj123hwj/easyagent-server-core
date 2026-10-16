// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

// Package command recognizes host conversation controls before model dispatch.
// It does not execute commands or grant permissions. Hosts authenticate users,
// persist idempotency/session generations, and cancel only their own AI runs.
package command

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Kind string

const (
	None    Kind = ""
	New     Kind = "new"
	Stop    Kind = "stop"
	Status  Kind = "status"
	Help    Kind = "help"
	Unknown Kind = "unknown"
	Invalid Kind = "invalid"
)

type Command struct {
	Kind Kind
	Name string
}
type Definition struct {
	Name string
	Kind Kind
}

func Definitions() []Definition {
	return []Definition{{"/new", New}, {"/reset", New}, {"/stop", Stop}, {"/cancel", Stop}, {"/status", Status}, {"/help", Help}}
}

// Parse recognizes only a leading slash. Unknown and invalid commands remain
// control input: hosts must return local help, never silently forward to a model.
// Built-ins intentionally take no arguments. Text containing a quoted/embedded
// slash remains normal text. The returned name contains only ASCII letters.
func Parse(text string) (Command, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return Command{}, false
	}
	if len(text) > 8192 || !utf8.ValidString(text) || strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && !unicode.IsSpace(r) }) {
		return Command{Kind: Invalid}, true
	}
	fields := strings.Fields(text)
	name := strings.ToLower(fields[0])
	valid := len(name) > 1 && len(name) <= 33
	for _, c := range name[1:] {
		if c < 'a' || c > 'z' {
			valid = false
		}
	}
	if !valid {
		return Command{Kind: Unknown}, true
	}
	for _, d := range Definitions() {
		if name == d.Name {
			if len(fields) > 1 {
				return Command{Kind: Invalid, Name: name}, true
			}
			return Command{Kind: d.Kind, Name: name}, true
		}
	}
	return Command{Kind: Unknown, Name: name}, true
}
