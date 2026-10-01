package ports

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidMerge    = errors.New("invalid memory merge request")
	ErrMergeScopeDrift = errors.New("memories to merge are not in the same scope")
)

// MergeRequest asks to merge duplicate memories into one canonical target. The
// duplicates are rejected but kept for lineage, linked to the target with a
// merged_into relation, and their evidence is unioned into the target.
type MergeRequest struct {
	TargetID     string
	DuplicateIDs []string
	ActorType    string
	ActorID      string
	RequestID    string
	At           time.Time
}

// MergeResult reports the canonical target and the retired duplicates.
type MergeResult struct {
	Target     MemoryNodeRecord
	Duplicates []MemoryNodeRecord
}

// MemoryMergeRepository unifies duplicate memories inside one tenant
// transaction. Merge is a distinct primitive from supersede: it accumulates
// evidence and raises confidence instead of replacing an obsolete version.
type MemoryMergeRepository interface {
	MergeMemories(ctx context.Context, tenantID string, request MergeRequest) (MergeResult, error)
}
