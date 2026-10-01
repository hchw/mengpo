package ontology

import (
	"errors"
	"testing"
)

func TestNormalizeCollapsesCaseAndWhitespace(t *testing.T) {
	for _, surface := range []string{"PostgreSQL", "postgresql", "  PostgreSQL  "} {
		if got := Normalize(surface); got != "postgresql" {
			t.Fatalf("Normalize(%q)=%q", surface, got)
		}
	}
}

func TestResolverResolvesAliasesAndRejectsAmbiguousOnes(t *testing.T) {
	pg, err := NewEntity("e1", "PostgreSQL", "database", []string{"postgres", "psql"})
	if err != nil {
		t.Fatal(err)
	}
	redis, err := NewEntity("e2", "Redis", "cache", nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver([]Entity{pg, redis})
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"postgres", "PSQL", " PostgreSQL "} {
		entity, ok := resolver.Resolve(surface)
		if !ok || entity.ID != "e1" {
			t.Fatalf("Resolve(%q)=%#v ok=%v", surface, entity, ok)
		}
	}
	if _, ok := resolver.Resolve("MySQL"); ok {
		t.Fatal("unknown entity resolved")
	}
	duplicate, _ := NewEntity("e3", "Another", "database", []string{"postgres"})
	if _, err := NewResolver([]Entity{pg, duplicate}); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("ambiguous alias err=%v", err)
	}
}

func TestRelationsPreserveUncertaintyAndConflicts(t *testing.T) {
	certain, err := NewRelation("api", "database", RelationDependsOn, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if certain.Uncertain {
		t.Fatal("high confidence relation marked uncertain")
	}
	uncertain, err := NewRelation("api", "cache", RelationDependsOn, 0.3)
	if err != nil || !uncertain.Uncertain {
		t.Fatalf("low confidence relation uncertain=%v err=%v", uncertain, err)
	}
	conflict, _ := NewRelation("api", "database", RelationConflictsWith, 0.8)
	conflicts := DetectConflicts([]Relation{certain, conflict, uncertain})
	if len(conflicts) != 1 || conflicts[0][0].Type == conflicts[0][1].Type {
		t.Fatalf("conflicts=%#v", conflicts)
	}
	if _, err := NewRelation("", "x", RelationIsA, 0.5); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("empty from err=%v", err)
	}
	if _, err := NewRelation("a", "b", RelationIsA, 1.5); !errors.Is(err, ErrInvalidRelation) {
		t.Fatalf("out-of-range confidence err=%v", err)
	}
}
