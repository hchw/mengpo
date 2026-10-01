// Package tools defines the agent-facing memory tool contracts. Each tool has a
// strict argument schema so an agent cannot smuggle scope, tenant or governance
// decisions through an untyped argument bag.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrUnknownTool     = errors.New("unknown memory tool")
	ErrInvalidToolArgs = errors.New("invalid memory tool arguments")
)

type ToolName string

const (
	MemoryContext   ToolName = "memory_context"
	MemoryRecall    ToolName = "memory_recall"
	MemoryFeedback  ToolName = "memory_feedback"
	MemoryTrace     ToolName = "memory_trace"
	MemoryConfirm   ToolName = "memory_confirm"
	MemoryCorrect   ToolName = "memory_correct"
	MemoryOpenLoops ToolName = "memory_open_loops"
)

type ArgumentSpec struct {
	Name     string
	Type     string // string | string[] | int | number | bool
	Required bool
	// Allowed restricts a string argument to a fixed value set.
	Allowed []string
}

type Contract struct {
	Name        ToolName
	Description string
	Arguments   []ArgumentSpec
	// Mutates reports whether the tool can change governance state.
	Mutates bool
}

// Contracts is the complete tool surface. memory_confirm and memory_correct are
// the only tools allowed to change governance state.
var Contracts = []Contract{
	{Name: MemoryContext, Description: "Project context for the current task", Arguments: []ArgumentSpec{
		{Name: "query", Type: "string", Required: true},
		{Name: "session_id", Type: "string"},
		{Name: "mode", Type: "string", Allowed: []string{"auto", "focus", "diverge"}},
	}},
	{Name: MemoryRecall, Description: "Recall candidate memories without injection", Arguments: []ArgumentSpec{
		{Name: "query", Type: "string", Required: true},
		{Name: "limit", Type: "int"},
	}},
	{Name: MemoryFeedback, Description: "Report feedback about an injected memory", Arguments: []ArgumentSpec{
		{Name: "memory_id", Type: "string", Required: true},
		{Name: "feedback_type", Type: "string", Required: true, Allowed: []string{"accepted", "helpful", "harmful", "corrected", "ignored", "conflict", "expired"}},
		{Name: "reason", Type: "string"},
	}},
	{Name: MemoryTrace, Description: "Explain why a memory was projected", Arguments: []ArgumentSpec{
		{Name: "memory_id", Type: "string", Required: true},
		{Name: "request_id", Type: "string"},
	}},
	{Name: MemoryConfirm, Description: "Confirm a candidate memory (governance mutation)", Mutates: true, Arguments: []ArgumentSpec{
		{Name: "memory_id", Type: "string", Required: true},
		{Name: "expected_version", Type: "int", Required: true},
	}},
	{Name: MemoryCorrect, Description: "Correct a memory's content (governance mutation)", Mutates: true, Arguments: []ArgumentSpec{
		{Name: "memory_id", Type: "string", Required: true},
		{Name: "expected_version", Type: "int", Required: true},
		{Name: "content", Type: "string", Required: true},
	}},
	{Name: MemoryOpenLoops, Description: "List unresolved open loops", Arguments: []ArgumentSpec{
		{Name: "session_id", Type: "string"},
		{Name: "limit", Type: "int"},
	}},
}

func ContractFor(name ToolName) (Contract, bool) {
	for _, contract := range Contracts {
		if contract.Name == name {
			return contract, true
		}
	}
	return Contract{}, false
}

// Validate checks the raw tool arguments against the tool's contract, rejecting
// unknown tools, missing required arguments, type mismatches and disallowed
// enum values.
func Validate(name ToolName, raw json.RawMessage) error {
	contract, ok := ContractFor(name)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownTool, name)
	}
	arguments := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return fmt.Errorf("%w: arguments must be a JSON object", ErrInvalidToolArgs)
		}
	}
	for _, spec := range contract.Arguments {
		value, present := arguments[spec.Name]
		if !present || string(value) == "null" {
			if spec.Required {
				return fmt.Errorf("%w: %s requires %q", ErrInvalidToolArgs, name, spec.Name)
			}
			continue
		}
		if err := validateValue(spec, value); err != nil {
			return fmt.Errorf("%w: %s.%s: %v", ErrInvalidToolArgs, name, spec.Name, err)
		}
	}
	return nil
}

func validateValue(spec ArgumentSpec, raw json.RawMessage) error {
	switch spec.Type {
	case "string":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("expected string")
		}
		if len(spec.Allowed) > 0 {
			for _, allowed := range spec.Allowed {
				if value == allowed {
					return nil
				}
			}
			return fmt.Errorf("value %q not allowed", value)
		}
		return nil
	case "int":
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("expected integer")
		}
		return nil
	case "number":
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("expected number")
		}
		return nil
	case "bool":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("expected boolean")
		}
		return nil
	case "string[]":
		var value []string
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("expected string array")
		}
		return nil
	default:
		return errors.New("unknown argument type")
	}
}
