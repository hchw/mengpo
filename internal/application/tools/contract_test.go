package tools

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestEveryDocumentedToolHasAContract(t *testing.T) {
	expected := []ToolName{MemoryContext, MemoryRecall, MemoryFeedback, MemoryTrace, MemoryConfirm, MemoryCorrect, MemoryOpenLoops}
	for _, name := range expected {
		contract, ok := ContractFor(name)
		if !ok || contract.Description == "" {
			t.Fatalf("tool %s missing contract", name)
		}
	}
	if len(Contracts) != len(expected) {
		t.Fatalf("contract count=%d want %d", len(Contracts), len(expected))
	}
}

func TestValidateAcceptsWellFormedArguments(t *testing.T) {
	cases := []struct {
		name ToolName
		args string
	}{
		{MemoryContext, `{"query":"tenant routing","mode":"diverge"}`},
		{MemoryRecall, `{"query":"q","limit":5}`},
		{MemoryFeedback, `{"memory_id":"m1","feedback_type":"corrected","reason":"wrong"}`},
		{MemoryTrace, `{"memory_id":"m1"}`},
		{MemoryConfirm, `{"memory_id":"m1","expected_version":3}`},
		{MemoryCorrect, `{"memory_id":"m1","expected_version":3,"content":"new"}`},
		{MemoryOpenLoops, `{"session_id":"s1","limit":10}`},
	}
	for _, tc := range cases {
		if err := Validate(tc.name, json.RawMessage(tc.args)); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestValidateRejectsBadArguments(t *testing.T) {
	cases := []struct {
		name ToolName
		args string
	}{
		{MemoryContext, `{}`},
		{MemoryContext, `{"query":123}`},
		{MemoryContext, `{"query":"q","mode":"force"}`},
		{MemoryFeedback, `{"memory_id":"m1","feedback_type":"maybe"}`},
		{MemoryConfirm, `{"memory_id":"m1"}`},
		{MemoryCorrect, `{"memory_id":"m1","expected_version":3}`},
		{MemoryRecall, `{"query":"q","limit":"many"}`},
		{MemoryRecall, `not-json`},
	}
	for _, tc := range cases {
		if err := Validate(tc.name, json.RawMessage(tc.args)); !errors.Is(err, ErrInvalidToolArgs) {
			t.Fatalf("%s %s err=%v, want invalid args", tc.name, tc.args, err)
		}
	}
	if err := Validate("memory_delete", json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown tool err=%v", err)
	}
}

func TestOnlyConfirmAndCorrectMutateGovernance(t *testing.T) {
	for _, contract := range Contracts {
		shouldMutate := contract.Name == MemoryConfirm || contract.Name == MemoryCorrect
		if contract.Mutates != shouldMutate {
			t.Fatalf("tool %s Mutates=%v want %v", contract.Name, contract.Mutates, shouldMutate)
		}
	}
}
