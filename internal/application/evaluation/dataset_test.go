package evaluation

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestLoadDatasetsFromTestdata(t *testing.T) {
	retrieval, err := LoadDataset(DatasetRetrieval, mustOpen(t, "testdata/retrieval.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if retrieval.Version != "2024-01-01" || retrieval.Len() != 2 || retrieval.Kind != DatasetRetrieval {
		t.Fatalf("retrieval dataset=%#v", retrieval)
	}
	if retrieval.Retrieval[0].ExpectedMemoryIDs[0] != "m1" || retrieval.Retrieval[1].Mode != "diverge" {
		t.Fatalf("retrieval samples=%#v", retrieval.Retrieval)
	}
	consolidation, err := LoadDataset(DatasetConsolidation, mustOpen(t, "testdata/consolidation.jsonl"))
	if err != nil || consolidation.Len() != 2 {
		t.Fatalf("consolidation=%#v err=%v", consolidation, err)
	}
	failure, err := LoadDataset(DatasetFailure, mustOpen(t, "testdata/failure.jsonl"))
	if err != nil || failure.Len() != 2 {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	if failure.Failure[1].ExpectedConfidence != "suspected" || failure.Failure[1].ExpectedAttribution != "unknown" {
		t.Fatalf("failure samples=%#v", failure.Failure)
	}
}

func TestLoadDatasetRejectsHeaderMismatchAndUnknownFields(t *testing.T) {
	_, err := LoadDataset(DatasetRetrieval, strings.NewReader(`{"version":"1","kind":"failure"}
{"id":"r1","tenant_id":"t","query":"q","expected_memory_ids":["m1"]}`))
	if !errors.Is(err, ErrInvalidDataset) {
		t.Fatalf("kind mismatch err=%v", err)
	}
	_, err = LoadDataset(DatasetRetrieval, strings.NewReader(`{"version":"1","kind":"retrieval"}
{"id":"r1","tenant_id":"t","query":"q","expected_memory_ids":["m1"],"surprise":true}`))
	if !errors.Is(err, ErrInvalidSample) {
		t.Fatalf("unknown field err=%v", err)
	}
}

func TestLoadDatasetDetectsContradictoryLabels(t *testing.T) {
	_, err := LoadDataset(DatasetRetrieval, strings.NewReader(`{"version":"1","kind":"retrieval"}
{"id":"r1","tenant_id":"t","query":"q","expected_memory_ids":["m1"],"forbidden_memory_ids":["m1"]}`))
	if !errors.Is(err, ErrInvalidSample) {
		t.Fatalf("contradictory labels err=%v", err)
	}
}

func TestLoadDatasetRejectsEmptyAndMalformedInput(t *testing.T) {
	if _, err := LoadDataset(DatasetFailure, strings.NewReader("")); !errors.Is(err, ErrInvalidDataset) {
		t.Fatalf("empty err=%v", err)
	}
	if _, err := LoadDataset(DatasetFailure, strings.NewReader(`{"version":"1","kind":"failure"}
{"id":"f1","tenant_id":"t","transcript":"x","expected_confidence":"certain","expected_attribution":"direct"}`)); !errors.Is(err, ErrInvalidSample) {
		t.Fatalf("bad confidence err=%v", err)
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
