package plain

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func step(stepType, next, onTrue, onFalse string) stepModel {
	s := stepModel{
		Type:      types.StringValue(stepType),
		Payload:   jsontypes.NewNormalizedValue(`{"version":1}`),
		Name:      types.StringNull(),
		Next:      types.StringNull(),
		OnTrue:    types.StringNull(),
		OnFalse:   types.StringNull(),
		PositionX: types.Float64Value(0),
		PositionY: types.Float64Value(0),
	}
	if next != "" {
		s.Next = types.StringValue(next)
	}
	if onTrue != "" {
		s.OnTrue = types.StringValue(onTrue)
	}
	if onFalse != "" {
		s.OnFalse = types.StringValue(onFalse)
	}

	return s
}

func TestTransitionKeysArity(t *testing.T) {
	// Plain reads transitions positionally, so arity per step type is load-bearing.
	tests := []struct {
		name string
		step stepModel
		want int
	}{
		{"condition has two branches", step("CONDITION", "", "a", "b"), 2},
		{"action has one", step("ACTION", "a", "", ""), 1},
		{"wait has one", step("WAIT", "a", "", ""), 1},
		{"terminal condition still has two slots", step("CONDITION", "", "", ""), 2},
		{"terminal action still has one slot", step("ACTION", "", "", ""), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(tt.step.transitionKeys()); got != tt.want {
				t.Errorf("transitionKeys() arity = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestStepInputKeepsArityWhenTargetsAreUnresolvable(t *testing.T) {
	// Arity is positional and must hold even if a target cannot be resolved,
	// or Plain would read the wrong slot as the branch.
	in := stepInput(step("CONDITION", "", "a", "b"), map[string]string{})

	if len(in.Transitions) != 2 {
		t.Fatalf("Transitions length = %d, want 2", len(in.Transitions))
	}
	for i, tr := range in.Transitions {
		if tr != nil {
			t.Errorf("Transitions[%d] = %v, want nil when unresolvable", i, *tr)
		}
	}
}

func TestNewStepIDFormat(t *testing.T) {
	// Plain accepts client-supplied step IDs but they must look like its own:
	// a "wfs_" prefix plus a 26-character ULID.
	seen := map[string]bool{}

	for i := 0; i < 100; i++ {
		id := newStepID()

		if got, want := len(id), len(stepIDPrefix)+26; got != want {
			t.Fatalf("newStepID() length = %d (%q), want %d", got, id, want)
		}
		if id[:len(stepIDPrefix)] != stepIDPrefix {
			t.Fatalf("newStepID() = %q, want %q prefix", id, stepIDPrefix)
		}
		if seen[id] {
			t.Fatalf("newStepID() returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestStepInputResolvesKeysToIDs(t *testing.T) {
	resolved := map[string]string{"a": "step_aaa", "b": "step_bbb"}
	in := stepInput(step("CONDITION", "", "a", "b"), resolved)

	if in.Transitions[0] == nil || *in.Transitions[0] != "step_aaa" {
		t.Errorf("on_true resolved to %v, want step_aaa", in.Transitions[0])
	}
	if in.Transitions[1] == nil || *in.Transitions[1] != "step_bbb" {
		t.Errorf("on_false resolved to %v, want step_bbb", in.Transitions[1])
	}
}

func TestStepInputKeepsTerminalBranchNull(t *testing.T) {
	// An omitted branch is terminal and must stay null, not become an empty string.
	in := stepInput(step("CONDITION", "", "a", ""), map[string]string{"a": "step_aaa"})

	if in.Transitions[1] != nil {
		t.Errorf("terminal on_false = %v, want nil", *in.Transitions[1])
	}
}

func TestGraphChanged(t *testing.T) {
	base := map[string]stepModel{
		"check": step("CONDITION", "", "act", ""),
		"act":   step("ACTION", "", "", ""),
	}
	start := types.StringValue("check")

	rewired := map[string]stepModel{
		"check": step("CONDITION", "", "", "act"), // moved to the false branch
		"act":   step("ACTION", "", "", ""),
	}

	added := map[string]stepModel{
		"check": step("CONDITION", "", "act", ""),
		"act":   step("ACTION", "", "", ""),
		"extra": step("ACTION", "", "", ""),
	}

	retyped := map[string]stepModel{
		"check": step("CONDITION", "", "act", ""),
		"act":   step("WAIT", "", "", ""),
	}

	// Payload edits do not restructure the graph, so they must not force the
	// unpublish/republish cycle.
	repayloaded := map[string]stepModel{
		"check": step("CONDITION", "", "act", ""),
		"act":   step("ACTION", "", "", ""),
	}
	changed := repayloaded["act"]
	changed.Payload = jsontypes.NewNormalizedValue(`{"version":1,"type":"add_note"}`)
	repayloaded["act"] = changed

	tests := []struct {
		name  string
		plan  map[string]stepModel
		start types.String
		want  bool
	}{
		{"identical", base, start, false},
		{"payload only", repayloaded, start, false},
		{"rewired branch", rewired, start, true},
		{"step added", added, start, true},
		{"step retyped", retyped, start, true},
		{"entry point moved", base, types.StringValue("act"), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := graphChanged(tt.plan, base, tt.start, start); got != tt.want {
				t.Errorf("graphChanged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStepsContentChangedCatchesPayloadEdits(t *testing.T) {
	// graphChanged deliberately ignores payloads, so something else has to notice
	// them or a payload-only edit would never be written.
	base := map[string]stepModel{"act": step("ACTION", "", "", "")}

	edited := map[string]stepModel{"act": step("ACTION", "", "", "")}
	changed := edited["act"]
	changed.Payload = jsontypes.NewNormalizedValue(`{"version":1,"type":"add_note"}`)
	edited["act"] = changed

	if graphChanged(edited, base, types.StringNull(), types.StringNull()) {
		t.Error("graphChanged() = true for a payload-only edit, want false")
	}
	if !stepsContentChanged(edited, base) {
		t.Error("stepsContentChanged() = false for a payload-only edit, want true")
	}
}

func TestSortedKeysIsDeterministic(t *testing.T) {
	// Steps go up in a fixed order so the same config produces byte-identical
	// requests, which keeps diffs against Plain readable and makes a failed
	// apply reproducible. Go's map iteration order must not leak into that.
	steps := map[string]stepModel{
		"zebra": step("ACTION", "", "", ""),
		"alpha": step("ACTION", "", "", ""),
		"mid":   step("ACTION", "", "", ""),
	}

	want := []string{"alpha", "mid", "zebra"}
	for i := 0; i < 50; i++ {
		got := sortedKeys(steps)
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("sortedKeys() = %v, want %v", got, want)
			}
		}
	}
}
