package plain

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Config-time validation is the difference between a clear diagnostic and a
// half-applied workflow, so each rule gets a case here. These run without
// credentials; the acceptance tests only confirm the wiring reaches them.
func TestValidateStepGraph(t *testing.T) {
	tests := []struct {
		name  string
		steps map[string]stepModel
		start types.String

		wantError string // substring of the expected summary; empty means valid
		wantPath  string // substring of the expected attribute path
	}{
		{
			name: "valid condition and action",
			steps: map[string]stepModel{
				"check": step("CONDITION", "", "act", ""),
				"act":   step("ACTION", "", "", ""),
			},
			start: types.StringValue("check"),
		},
		{
			name:  "no steps and no entry point is valid",
			steps: map[string]stepModel{},
			start: types.StringNull(),
		},
		{
			name:      "steps without an entry point",
			steps:     map[string]stepModel{"act": step("ACTION", "", "", "")},
			start:     types.StringNull(),
			wantError: "start_step is required",
			wantPath:  "start_step",
		},
		{
			name:      "entry point names a step that does not exist",
			steps:     map[string]stepModel{"act": step("ACTION", "", "", "")},
			start:     types.StringValue("ghost"),
			wantError: "start_step does not match any step",
			wantPath:  "start_step",
		},
		{
			name: "next on a CONDITION step",
			steps: map[string]stepModel{
				"check": step("CONDITION", "act", "", ""),
				"act":   step("ACTION", "", "", ""),
			},
			start:     types.StringValue("check"),
			wantError: "next is not valid on a CONDITION step",
			wantPath:  "next",
		},
		{
			name: "on_true on an ACTION step",
			steps: map[string]stepModel{
				"act":   step("ACTION", "", "other", ""),
				"other": step("ACTION", "", "", ""),
			},
			start:     types.StringValue("act"),
			wantError: "on_true is only valid on a CONDITION step",
			wantPath:  "on_true",
		},
		{
			name: "on_false on a WAIT step",
			steps: map[string]stepModel{
				"hold":  step("WAIT", "", "", "other"),
				"other": step("ACTION", "", "", ""),
			},
			start:     types.StringValue("hold"),
			wantError: "on_false is only valid on a CONDITION step",
			wantPath:  "on_false",
		},
		{
			name:      "next points at nothing",
			steps:     map[string]stepModel{"act": step("ACTION", "ghost", "", "")},
			start:     types.StringValue("act"),
			wantError: "Transition target does not match any step",
			wantPath:  `steps["act"].next`,
		},
		{
			name: "on_false points at nothing",
			steps: map[string]stepModel{
				"check": step("CONDITION", "", "act", "ghost"),
				"act":   step("ACTION", "", "", ""),
			},
			start:     types.StringValue("check"),
			wantError: "Transition target does not match any step",
			// The path must name on_false, not on_true: they share a positional
			// array and reporting the wrong one sends people to the wrong line.
			wantPath: `steps["check"].on_false`,
		},
		{
			name:  "terminal branches are valid",
			steps: map[string]stepModel{"check": step("CONDITION", "", "", "")},
			start: types.StringValue("check"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := validateStepGraph(tt.steps, tt.start)

			if tt.wantError == "" {
				if diags.HasError() {
					t.Fatalf("validateStepGraph() reported errors on a valid graph: %s", diags)
				}
				return
			}

			if !diags.HasError() {
				t.Fatalf("validateStepGraph() found no error, want one mentioning %q", tt.wantError)
			}

			var summaries, paths []string
			for _, d := range diags.Errors() {
				summaries = append(summaries, d.Summary())
				if withPath, ok := d.(diag.DiagnosticWithPath); ok {
					paths = append(paths, withPath.Path().String())
				}
			}

			if !containsSubstring(summaries, tt.wantError) {
				t.Errorf("diagnostics %v do not mention %q", summaries, tt.wantError)
			}
			if tt.wantPath != "" && !containsSubstring(paths, tt.wantPath) {
				t.Errorf("diagnostic paths %v do not include %q", paths, tt.wantPath)
			}
		})
	}
}

// TestValidateStepGraphReportsEveryProblem checks validation does not stop at
// the first fault. Fixing config one error per plan is miserable.
func TestValidateStepGraphReportsEveryProblem(t *testing.T) {
	steps := map[string]stepModel{
		"a": step("ACTION", "ghost", "", ""),
		"b": step("ACTION", "alsoghost", "", ""),
	}

	diags := validateStepGraph(steps, types.StringValue("a"))
	if got := len(diags.Errors()); got != 2 {
		t.Fatalf("validateStepGraph() returned %d errors, want 2: %s", got, diags)
	}
}

func containsSubstring(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.Contains(s, needle) {
			return true
		}
	}

	return false
}
