// Package ontology normalizes entity names, resolves aliases and represents
// uncertain entity relations. Normalization must be deterministic and must not
// discard uncertainty: an unresolved entity stays explicit rather than being
// silently merged.
package ontology

import (
	"errors"
	"sort"
	"strings"
	"unicode"
)

var (
	ErrInvalidEntity   = errors.New("invalid ontology entity")
	ErrInvalidRelation = errors.New("invalid ontology relation")
)

// Entity is one normalized entity with its aliases. Canonical is the stable
// name; aliases are alternate surface forms that resolve to it.
type Entity struct {
	ID        string
	Canonical string
	Type      string
	Aliases   []string
}

// Normalize lower-cases, trims and collapses internal whitespace so that
// " PostgreSQL ", "postgresql" and "PostgreSQL" normalize identically.
func Normalize(name string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(name), unicode.IsSpace), " ")
}

func NewEntity(id, canonical, entityType string, aliases []string) (Entity, error) {
	if id == "" || Normalize(canonical) == "" || entityType == "" {
		return Entity{}, ErrInvalidEntity
	}
	cleaned := make([]string, 0, len(aliases))
	seen := map[string]struct{}{}
	for _, alias := range aliases {
		normalized := Normalize(alias)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		cleaned = append(cleaned, normalized)
	}
	sort.Strings(cleaned)
	return Entity{ID: id, Canonical: Normalize(canonical), Type: entityType, Aliases: cleaned}, nil
}

// Resolver indexes entities by canonical and alias form.
type Resolver struct {
	bySurface map[string]Entity
}

func NewResolver(entities []Entity) (*Resolver, error) {
	resolver := &Resolver{bySurface: map[string]Entity{}}
	for _, entity := range entities {
		if entity.ID == "" || entity.Canonical == "" {
			return nil, ErrInvalidEntity
		}
		for _, surface := range append([]string{entity.Canonical}, entity.Aliases...) {
			normalized := Normalize(surface)
			if existing, ok := resolver.bySurface[normalized]; ok && existing.ID != entity.ID {
				return nil, ErrInvalidEntity // ambiguous alias across entities
			}
			resolver.bySurface[normalized] = entity
		}
	}
	return resolver, nil
}

// Resolve returns the canonical entity for any surface form. Unknown names are
// reported as unresolved rather than guessed.
func (r *Resolver) Resolve(name string) (Entity, bool) {
	entity, ok := r.bySurface[Normalize(name)]
	return entity, ok
}

// RelationType configures how strongly a relation is held.
type RelationType string

const (
	RelationIsA           RelationType = "is_a"
	RelationPartOf        RelationType = "part_of"
	RelationDependsOn     RelationType = "depends_on"
	RelationConflictsWith RelationType = "conflicts_with"
)

// Relation is a directed edge. Uncertain relations keep Uncertain=true and a
// reduced confidence so consumers never treat a guess as a fact.
type Relation struct {
	From       string
	To         string
	Type       RelationType
	Confidence float64
	Uncertain  bool
}

func NewRelation(from, to string, relationType RelationType, confidence float64) (Relation, error) {
	if Normalize(from) == "" || Normalize(to) == "" || confidence < 0 || confidence > 1 {
		return Relation{}, ErrInvalidRelation
	}
	return Relation{From: Normalize(from), To: Normalize(to), Type: relationType, Confidence: confidence, Uncertain: confidence < 0.5}, nil
}

// Conflicts reports whether two relations disagree about the same pair.
func Conflicts(left, right Relation) bool {
	if left.From != right.From || left.To != right.To {
		return false
	}
	return left.Type != right.Type
}

// DetectConflicts returns every conflicting pair, keeping both relations so the
// conflict itself is preserved instead of one side being dropped.
func DetectConflicts(relations []Relation) [][2]Relation {
	var conflicts [][2]Relation
	for i := 0; i < len(relations); i++ {
		for j := i + 1; j < len(relations); j++ {
			if Conflicts(relations[i], relations[j]) {
				conflicts = append(conflicts, [2]Relation{relations[i], relations[j]})
			}
		}
	}
	return conflicts
}
