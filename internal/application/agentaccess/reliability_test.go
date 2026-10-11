package agentaccess

import (
	"testing"

	obsdomain "github.com/hchw/mengpo/internal/domain/observation"
)

func TestObserveReliabilityAssignsHighOnlyToVerifiableSources(t *testing.T) {
	cases := []struct {
		source      obsdomain.SourceType
		messageType string
		want        obsdomain.Reliability
	}{
		{obsdomain.SourceUser, "user_remember", obsdomain.ReliabilityHigh},
		{obsdomain.SourceTool, "tool.result", obsdomain.ReliabilityHigh},
		{obsdomain.SourceTool, "tool.failure", obsdomain.ReliabilityHigh},
		{obsdomain.SourceAgent, "turn.outcome", obsdomain.ReliabilityHigh},
		{obsdomain.SourceUser, "message", obsdomain.ReliabilityUnknown},
		{obsdomain.SourceWorkflow, "context.compaction", obsdomain.ReliabilityUnknown},
		{obsdomain.SourceAgent, "", obsdomain.ReliabilityUnknown},
	}
	for _, test := range cases {
		if got := observeReliability(test.source, test.messageType); got != test.want {
			t.Fatalf("observeReliability(%q, %q) = %q, want %q", test.source, test.messageType, got, test.want)
		}
	}
}
