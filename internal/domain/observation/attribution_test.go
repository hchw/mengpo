package observation

import (
	"reflect"
	"testing"
)

func TestAttributionLevelsReflectAvailableLinkage(t *testing.T) {
	complete := Trace{TaskID: "task", AttemptID: "attempt", ProjectionID: "projection", UsedMemoryIDs: []string{"memory"}, ToolResultID: "tool-result", OutcomeID: "outcome"}
	cases := []struct {
		name, session, conversation, parent string
		trace                               Trace
		level                               AttributionLevel
	}{
		{"deep direct", "s", "", "", complete, AttributionDirect},
		{"partial trace direct", "s", "", "", Trace{TaskID: "task", ToolResultID: "tool-result", OutcomeID: "outcome"}, AttributionDirect},
		{"parent link direct", "s", "", "parent", Trace{ToolResultID: "tool-result", OutcomeID: "outcome"}, AttributionDirect},
		{"trace correlated", "s", "", "", Trace{TaskID: "task"}, AttributionCorrelated},
		{"session inferred", "s", "", "", Trace{}, AttributionInferred},
		{"conversation inferred", "", "c", "", Trace{}, AttributionInferred},
		{"unlinked unknown", "", "", "", Trace{}, AttributionUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessAttribution(tc.session, tc.conversation, tc.parent, tc.trace)
			if got.Level != tc.level {
				t.Fatalf("level=%s want %s", got.Level, tc.level)
			}
			if tc.level == AttributionUnknown && !reflect.DeepEqual(got.Limitations[:1], []string{"session_or_conversation_id"}) {
				t.Fatalf("limitations=%v", got.Limitations)
			}
		})
	}
}
