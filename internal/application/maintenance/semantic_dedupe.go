package maintenance

import (
	"context"

	"github.com/hchw/mengpo/internal/ports"
)

// groupDuplicates clusters nodes into duplicate groups. Two nodes share a group
// when their canonical content texts are byte-identical or when they are
// connected (transitively) by semantic near-duplicate edges. Group ids are the
// smallest member node id, so the grouping is deterministic and stable across
// runs.
func groupDuplicates(nodes []ports.MemoryNodeRecord, adjacency map[string][]string) map[string]string {
	parent := make(map[string]string, len(nodes))
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	for _, node := range nodes {
		parent[node.ID] = node.ID
	}
	byKey := make(map[string]string, len(nodes))
	for _, node := range nodes {
		key := duplicateKey(node)
		if existing, ok := byKey[key]; ok {
			union(existing, node.ID)
		} else {
			byKey[key] = node.ID
		}
	}
	for id, neighbors := range adjacency {
		if _, ok := parent[id]; !ok {
			continue
		}
		for _, other := range neighbors {
			if _, ok := parent[other]; !ok {
				continue
			}
			union(id, other)
		}
	}
	groups := make(map[string]string, len(nodes))
	for _, node := range nodes {
		groups[node.ID] = find(node.ID)
	}
	return groups
}

// distinctGroups counts the groups in a nodeID->groupID map.
func distinctGroups(groups map[string]string) int {
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		seen[group] = struct{}{}
	}
	return len(seen)
}

// semanticAdjacency builds id -> near-duplicate ids restricted to the node set.
// A node without a ready embedding contributes no edges (degradation), so it is
// never merged on similarity alone; a byte-identical peer still groups it via
// the content key. A nil reader or non-positive threshold yields no edges.
func semanticAdjacency(ctx context.Context, reader ports.SemanticNeighborReader, tenantID string, nodes []ports.MemoryNodeRecord, threshold float64) (map[string][]string, error) {
	if reader == nil || threshold <= 0 || len(nodes) < 2 {
		return nil, nil
	}
	known := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		known[node.ID] = struct{}{}
	}
	adjacency := make(map[string][]string, len(nodes))
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		neighbors, err := reader.ListSemanticNeighbors(ctx, tenantID, ports.SemanticNeighborQuery{
			ReferenceMemoryID: node.ID, Threshold: threshold, Limit: len(nodes),
		})
		if err != nil {
			return nil, err
		}
		for _, neighbor := range neighbors {
			if _, ok := known[neighbor.Node.ID]; !ok {
				continue
			}
			adjacency[node.ID] = append(adjacency[node.ID], neighbor.Node.ID)
		}
	}
	return adjacency, nil
}
