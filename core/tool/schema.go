// Copyright (c) 2026 hwj123hwj
// SPDX-License-Identifier: MIT

package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrArguments = errors.New("invalid tool arguments")

// Schema is a deliberately small, closed JSON Schema subset. All objects reject
// unknown properties. Unsupported keywords are not accepted through a raw schema.
// Resource ownership, dates and business state still belong to the host.
type Schema struct {
	Type       string            `json:"type"`
	Properties map[string]Schema `json:"properties,omitempty"`
	Required   []string          `json:"required,omitempty"`
	Items      *Schema           `json:"items,omitempty"`
	Enum       []string          `json:"enum,omitempty"`
	MinLength  int               `json:"minLength,omitempty"`
	MaxLength  int               `json:"maxLength,omitempty"`
	MaxItems   int               `json:"maxItems,omitempty"`
}

func (s Schema) Check() error { return s.check(0) }

func (s Schema) check(depth int) error {
	if depth > 16 || s.MinLength < 0 || s.MaxLength < 0 || s.MaxItems < 0 || (s.MaxLength > 0 && s.MinLength > s.MaxLength) {
		return ErrArguments
	}
	if s.Type != "object" && (len(s.Properties) > 0 || len(s.Required) > 0) {
		return ErrArguments
	}
	if s.Type != "array" && (s.Items != nil || s.MaxItems != 0) {
		return ErrArguments
	}
	if s.Type != "string" && (len(s.Enum) > 0 || s.MinLength != 0 || s.MaxLength != 0) {
		return ErrArguments
	}
	switch s.Type {
	case "object":
		if s.Items != nil {
			return ErrArguments
		}
		for _, k := range s.Required {
			if _, ok := s.Properties[k]; !ok {
				return ErrArguments
			}
		}
		for k, p := range s.Properties {
			if k == "" || !utf8.ValidString(k) || p.check(depth+1) != nil {
				return ErrArguments
			}
		}
	case "array":
		if s.Items == nil || s.Items.check(depth+1) != nil {
			return ErrArguments
		}
	case "string", "integer", "number", "boolean":
	default:
		return ErrArguments
	}
	return nil
}

// JSON produces a provider schema, explicitly closing every object.
func (s Schema) JSON() json.RawMessage {
	var object map[string]any
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &object)
	if s.Type == "object" {
		object["additionalProperties"] = false
		props := make(map[string]json.RawMessage, len(s.Properties))
		for k, p := range s.Properties {
			props[k] = p.JSON()
		}
		object["properties"] = props
	}
	if s.Items != nil {
		object["items"] = s.Items.JSON()
	}
	b, _ = json.Marshal(object)
	return b
}

// Normalize validates types and rejects duplicate keys, trailing JSON and invalid
// UTF-8 before producing stable key ordering for policy and approval binding.
func (s Schema) Normalize(raw json.RawMessage) (json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, ErrArguments
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := decodeValue(d, 0)
	if err != nil || s.validate(v) != nil {
		return nil, ErrArguments
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrArguments
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, ErrArguments
	}
	return out, nil
}

func decodeValue(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, ErrArguments
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, ErrArguments
				}
				if _, exists := m[k]; exists {
					return nil, ErrArguments
				}
				v, err := decodeValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrArguments
			}
			return m, nil
		case '[':
			values := []any{}
			for d.More() {
				v, err := decodeValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				values = append(values, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrArguments
			}
			return values, nil
		default:
			return nil, ErrArguments
		}
	}
	return t, nil
}

func (s Schema) validate(v any) error {
	switch s.Type {
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return ErrArguments
		}
		for _, k := range s.Required {
			if _, ok := m[k]; !ok {
				return ErrArguments
			}
		}
		for k, value := range m {
			p, ok := s.Properties[k]
			if !ok || p.validate(value) != nil {
				return ErrArguments
			}
		}
	case "array":
		a, ok := v.([]any)
		if !ok || (s.MaxItems > 0 && len(a) > s.MaxItems) || s.Items == nil {
			return ErrArguments
		}
		for _, value := range a {
			if s.Items.validate(value) != nil {
				return ErrArguments
			}
		}
	case "string":
		x, ok := v.(string)
		if !ok {
			return ErrArguments
		}
		n := utf8.RuneCountInString(x)
		if n < s.MinLength || (s.MaxLength > 0 && n > s.MaxLength) {
			return ErrArguments
		}
		if len(s.Enum) > 0 {
			for _, item := range s.Enum {
				if x == item {
					return nil
				}
			}
			return ErrArguments
		}
	case "integer", "number":
		x, ok := v.(json.Number)
		if !ok {
			return ErrArguments
		}
		// Bound exact arithmetic: hostile exponents must not allocate enormous integers.
		if len(x) > 128 {
			return ErrArguments
		}
		if index := strings.IndexAny(string(x), "eE"); index >= 0 {
			exponent, err := strconv.Atoi(string(x)[index+1:])
			if err != nil || exponent < -308 || exponent > 308 {
				return ErrArguments
			}
		}
		n, ok := new(big.Rat).SetString(string(x))
		if !ok || (s.Type == "integer" && !n.IsInt()) {
			return ErrArguments
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return ErrArguments
		}
	default:
		return ErrArguments
	}
	return nil
}
