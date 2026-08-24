package plain

import (
	"context"
	"testing"
)

// TestSetRulePublishedIsANoOpWhenAlreadyCorrect pins the guard that makes
// toggleWorkflowRulePublished safe to use.
//
// Plain gives rules a toggle rather than an isPublished field, so firing it
// unconditionally would invert the state every time Update ran — publishing a
// draft the practitioner never asked to publish. The resource is built with a
// nil client here: if the guard were removed, the call would panic rather than
// silently pass.
func TestSetRulePublishedIsANoOpWhenAlreadyCorrect(t *testing.T) {
	r := &workflowRuleResource{}

	for _, state := range []bool{true, false} {
		if err := r.setRulePublished(context.Background(), "wfr_test", state, state); err != nil {
			t.Errorf("setRulePublished(have=%t, want=%t) = %v, want no call and no error", state, state, err)
		}
	}
}
