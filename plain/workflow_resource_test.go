package plain

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework-jsontypes/jsontypes"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// emptyWorkflowState builds a null state from the resource schema, so
// SetAttribute has somewhere to write.
func emptyWorkflowState(t *testing.T, ctx context.Context, r fwresource.Resource) tfsdk.State {
	t.Helper()

	resp := &fwresource.SchemaResponse{}
	r.Schema(ctx, fwresource.SchemaRequest{}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}

	return tfsdk.State{
		Schema: resp.Schema,
		Raw:    tftypes.NewValue(resp.Schema.Type().TerraformType(ctx), nil),
	}
}

// TestImportStateRejectsNonWorkflowID covers the shape guard on the import
// address. The rejecting cases return before touching resp.State, so a
// zero-value ImportStateResponse is enough; the accepting cases need a real
// tfsdk.State built from the resource schema so SetAttribute has somewhere to
// write.
func TestImportStateRejectsNonWorkflowID(t *testing.T) {
	ctx := context.Background()

	r := NewWorkflowResource()

	tests := []struct {
		name    string
		id      string
		wantErr bool
		// wantID is the value expected in state when the address is accepted.
		wantID string
	}{
		{name: "empty", id: "", wantErr: true},
		{name: "not an id", id: "banana", wantErr: true},
		{name: "step id", id: stepIDPrefix + "01HXXXXXXXXXXXXXXXXXXXXXXX", wantErr: true},
		{name: "whitespace only", id: "   ", wantErr: true},
		{name: "padded workflow id", id: "  wf_01HXXXXXXXXXXXXXXXXXXXXXXX  ", wantID: "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"},
		{name: "workflow id", id: "wf_01HXXXXXXXXXXXXXXXXXXXXXXX", wantID: "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// resp.Identity is deliberately left nil here: the framework always
			// populates it, but setIdentity tolerates a nil and this is the only
			// test that exercises that path.
			resp := &fwresource.ImportStateResponse{State: emptyWorkflowState(t, ctx, r)}
			r.(fwresource.ResourceWithImportState).ImportState(ctx, fwresource.ImportStateRequest{ID: tc.id}, resp)

			if got := resp.Diagnostics.HasError(); got != tc.wantErr {
				t.Fatalf("HasError() = %t, want %t (diags: %v)", got, tc.wantErr, resp.Diagnostics)
			}
			if tc.wantErr {
				// The message must name the attribute shape, not echo the API.
				detail := resp.Diagnostics.Errors()[0].Detail()
				if !strings.Contains(detail, workflowIDPrefix) {
					t.Errorf("detail does not mention %q: %s", workflowIDPrefix, detail)
				}
				// The untrimmed input is quoted so whitespace stays visible.
				if !strings.Contains(detail, strconv.Quote(tc.id)) {
					t.Errorf("detail does not quote the raw ID %q: %s", tc.id, detail)
				}
				// A step ID gets the extra "steps are not importable" paragraph.
				wantStepHint := strings.HasPrefix(strings.TrimSpace(tc.id), stepIDPrefix)
				if got := strings.Contains(detail, "not separately"); got != wantStepHint {
					t.Errorf("step-ID hint present = %t, want %t: %s", got, wantStepHint, detail)
				}
				return
			}

			var gotID types.String
			if diags := resp.State.GetAttribute(ctx, path("id"), &gotID); diags.HasError() {
				t.Fatalf("reading id back: %v", diags)
			}
			if gotID.ValueString() != tc.wantID {
				t.Errorf("state id = %q, want %q", gotID.ValueString(), tc.wantID)
			}
		})
	}
}

// TestMatchEmptyStepsShape covers the null-versus-empty distinction Terraform
// enforces on an Optional attribute. read collapses "no steps" to null, which
// is right for a config that omits steps and wrong for one that writes
// `steps = {}` — the latter fails the apply with "Provider produced
// inconsistent result after apply" unless the shape is restored.
func TestMatchEmptyStepsShape(t *testing.T) {
	objType := types.ObjectType{AttrTypes: stepAttrTypes()}
	emptyMap := types.MapValueMust(objType, map[string]attr.Value{})
	nullMap := types.MapNull(objType)
	unknownMap := types.MapUnknown(objType)

	populated := types.MapValueMust(objType, map[string]attr.Value{
		"only": types.ObjectValueMust(stepAttrTypes(), map[string]attr.Value{
			"id":         types.StringValue("wfs_01"),
			"type":       types.StringValue("ACTION"),
			"name":       types.StringValue("Raise priority"),
			"payload":    jsontypes.NewNormalizedValue(`{"type":"set_priority"}`),
			"next":       types.StringNull(),
			"on_true":    types.StringNull(),
			"on_false":   types.StringNull(),
			"position_x": types.Float64Value(0),
			"position_y": types.Float64Value(0),
		}),
	})

	for _, tc := range []struct {
		name         string
		prior, fresh types.Map
		want         types.Map
	}{
		// The case this exists for: config wrote an empty map, Plain has no
		// steps, so read produced null. Must come back as an empty map.
		{"empty prior, null fresh", emptyMap, nullMap, emptyMap},

		// Config omitted steps entirely. Null is correct and must be left alone.
		{"null prior, null fresh", nullMap, nullMap, nullMap},

		// Nothing to restore: the workflow has steps.
		{"empty prior, populated fresh", emptyMap, populated, populated},
		{"null prior, populated fresh", nullMap, populated, populated},
		{"populated prior, populated fresh", populated, populated, populated},

		// A workflow whose steps were removed out of band, from a config that
		// declared them. Null is the honest answer; the plan will show the diff.
		{"populated prior, null fresh", populated, nullMap, nullMap},

		// An unknown prior carries no shape to preserve.
		{"unknown prior, null fresh", unknownMap, nullMap, nullMap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := matchEmptyStepsShape(tc.prior, tc.fresh)
			if !got.Equal(tc.want) {
				t.Errorf("matchEmptyStepsShape(%v, %v) = %v, want %v", tc.prior, tc.fresh, got, tc.want)
			}
		})
	}
}
