package plain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// workflowConfig renders a two-step workflow: a priority check that raises
// urgent threads and lets everything else fall through.
//
// The payloads here are the shapes verified against the live API — note that
// priority_equals takes an array and set_priority takes a scalar.
func workflowConfig(name string, published bool, priority int, onTrue, onFalse string) string {
	return workflowConfigWith(workflowOptions{
		name:      name,
		published: published,
		priority:  priority,
		onTrue:    onTrue,
		onFalse:   onFalse,
	})
}

// workflowOptions is the full parameter set for that same two-step workflow.
// The cosmetic attributes live here rather than on workflowConfig's signature
// because five positional arguments is already at the limit of what a reader
// can keep straight, and a name plus an x and a y for each of two steps would
// have made it ten.
//
// The zero value renders byte-for-byte what workflowConfig has always
// rendered, and that is load-bearing rather than tidy: terraform-plugin-testing
// compares step configs as text, so any drift in the rendering — a coordinate
// written as 0 instead of omitted, say — would turn every PlanOnly step in this
// file into a spurious diff.
type workflowOptions struct {
	name      string
	published bool

	// priority is what the CONDITION step's priority_equals payload matches on,
	// so changing it alone is a payload-only edit.
	priority int

	onTrue  string
	onFalse string

	// condName overrides the CONDITION step's name. Empty keeps the default.
	condName string

	// condPos and actionPos place the two steps on Plain's canvas. nil leaves
	// position_x/position_y out of the HCL altogether, which is not the same as
	// setting them to 0: omitted, the schema's float64default supplies the value
	// and the practitioner-supplied path is never exercised.
	condPos   *canvasPos
	actionPos *canvasPos
}

// canvasPos is one step's position on Plain's workflow canvas.
//
// Whole numbers only. The schema attributes are Float64 and Terraform carries
// them into state as the JSON text of the number, so an integral coordinate is
// the only kind whose TestCheckResourceAttr string can be written without
// guessing at a float format.
type canvasPos struct {
	x, y int
}

// defaultCondStepName is the CONDITION step's name unless a test overrides it.
const defaultCondStepName = "Is the thread urgent?"

func workflowConfigWith(opts workflowOptions) string {
	branch := func(attr, target string) string {
		if target == "" {
			return ""
		}
		return fmt.Sprintf("      %s = %q\n", attr, target)
	}

	position := func(p *canvasPos) string {
		if p == nil {
			return ""
		}
		return fmt.Sprintf("      position_x = %d\n      position_y = %d\n", p.x, p.y)
	}

	condName := opts.condName
	if condName == "" {
		condName = defaultCondStepName
	}

	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %[1]q
  published = %[2]t

  trigger = jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })

  start_step = "is_urgent"

  steps = {
    is_urgent = {
      type = "CONDITION"
      name = %[6]q

      payload = jsonencode({
        version    = 1
        type       = "priority_equals"
        priorities = [%[3]d]
      })

%[4]s%[5]s%[7]s    }

    raise = {
      type = "ACTION"
      name = "Raise priority"

      payload = jsonencode({
        version  = 1
        type     = "set_priority"
        priority = 0
      })
%[8]s    }
  }
}
`, opts.name, opts.published, opts.priority,
		branch("on_true", opts.onTrue), branch("on_false", opts.onFalse),
		condName, position(opts.condPos), position(opts.actionPos))
}

// workflowThreeStepConfig adds a WAIT step, which is how the tests exercise
// adding and removing steps from an existing graph.
func workflowThreeStepConfig(name string, published bool) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %q
  published = %t

  trigger = jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })

  start_step = "is_urgent"

  steps = {
    is_urgent = {
      type = "CONDITION"
      name = "Is the thread urgent?"

      payload = jsonencode({
        version    = 1
        type       = "priority_equals"
        priorities = [0]
      })

      on_true  = "raise"
      on_false = "hold"
    }

    raise = {
      type = "ACTION"
      name = "Raise priority"

      payload = jsonencode({
        version  = 1
        type     = "set_priority"
        priority = 0
      })
    }

    hold = {
      type = "WAIT"
      name = "Hold briefly"

      payload = jsonencode({
        duration = 60
      })
    }
  }
}
`, name, published)
}

// rekeyedConfig renders the same two-step graph as workflowConfig but with the
// step keys supplied by the caller, so a test can apply one set of keys and
// then rename them. workflowConfig hard-codes is_urgent/raise.
func rekeyedConfig(name string, published bool, condKey, actionKey string) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %[1]q
  published = %[2]t

  trigger = jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })

  start_step = %[3]q

  steps = {
    %[3]q = {
      type = "CONDITION"
      name = "Is the thread urgent?"

      payload = jsonencode({
        version    = 1
        type       = "priority_equals"
        priorities = [0]
      })

      on_true = %[4]q
    }

    %[4]q = {
      type = "ACTION"
      name = "Raise priority"

      payload = jsonencode({
        version  = 1
        type     = "set_priority"
        priority = 0
      })
    }
  }
}
`, name, published, condKey, actionKey)
}

// TestAccWorkflow_basic covers the whole ordinary lifecycle: create a draft,
// read it back cleanly, rename it, and destroy it.
//
// It is also the only test that gives the steps canvas coordinates. Everywhere
// else position_x/position_y are omitted, so the schema's
// float64default.StaticFloat64(0) supplies them and a coordinate a practitioner
// actually wrote never travels to Plain and back. Here they are set on create,
// carried unchanged through the rename, and then moved.
func TestAccWorkflow_basic(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow")
	renamed := name + "-renamed"

	placed := workflowOptions{
		name:      name,
		onTrue:    "raise",
		condPos:   &canvasPos{x: 120, y: 40},
		actionPos: &canvasPos{x: 360, y: 40},
	}

	// The rename leaves the coordinates alone, so that step still isolates the
	// name change.
	renamedAndPlaced := placed
	renamedAndPlaced.name = renamed

	// And then only the coordinates move.
	moved := renamedAndPlaced
	moved.condPos = &canvasPos{x: 200, y: 90}
	moved.actionPos = &canvasPos{x: 440, y: 90}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				Config: workflowConfigWith(placed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "name", name),
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "false"),
					resource.TestCheckResourceAttr("plain_workflow.test", "start_step", "is_urgent"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.type", "CONDITION"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_true", "raise"),
					// An omitted branch is terminal and must stay null rather than
					// becoming an empty string.
					resource.TestCheckNoResourceAttr("plain_workflow.test", "steps.is_urgent.on_false"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.type", "ACTION"),
					resource.TestCheckResourceAttrSet("plain_workflow.test", "id"),
					// Non-default coordinates have to survive the write and the
					// read-back. A provider that dropped them would still pass every
					// other check in this file, because every other config leaves them
					// at the default.
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_x", "120"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_y", "40"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_x", "360"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_y", "40"),
					// Steps carry Plain's assigned IDs even though they are keyed locally.
					resource.TestCheckResourceAttrWith("plain_workflow.test", "steps.raise.id", func(v string) error {
						if !strings.HasPrefix(v, stepIDPrefix) {
							return fmt.Errorf("step id %q does not start with %q", v, stepIDPrefix)
						}
						return nil
					}),
				),
			},
			{
				// A second plan against the same config must be empty. This is what
				// catches JSON normalization drift in trigger and payload — and now
				// also a coordinate that comes back from Plain in a different shape
				// than it went up in.
				Config:   workflowConfigWith(placed),
				PlanOnly: true,
			},
			{
				Config: workflowConfigWith(renamedAndPlaced),
				Check:  resource.TestCheckResourceAttr("plain_workflow.test", "name", renamed),
			},
			{
				// A coordinate-only edit. stepsContentChanged is what has to notice
				// it: nothing about the graph moved, so if the provider compared only
				// payloads the new positions would never be written and this would
				// fail on the read-back.
				Config: workflowConfigWith(moved),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_x", "200"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_y", "90"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_x", "440"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_y", "90"),
				),
			},
		},
	})
}

// TestAccWorkflow_import proves the documented import behaviour rather than
// working around it: Plain does not store the local step keys, so an imported
// workflow comes back keyed by Plain's step IDs.
//
// Both lifecycle states are covered. A published workflow reads its published
// flag back from publishedAt rather than from anything the practitioner wrote,
// so importing one exercises a different path through read than a draft does —
// and the published case is the one a practitioner is most likely to hit, since
// it is the live automation they are adopting into Terraform.
func TestAccWorkflow_import(t *testing.T) {
	for _, tt := range []struct {
		name      string
		published bool
	}{
		{"draft", false},
		{"published", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name := acctest.RandomWithPrefix("tf-acc-workflow-import-" + tt.name)
			config := workflowConfig(name, tt.published, 0, "raise", "")

			resource.Test(t, resource.TestCase{
				PreCheck:                 func() { testAccPreCheck(t) },
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				CheckDestroy:             testAccCheckWorkflowDestroyed,
				Steps: []resource.TestStep{
					{
						Config: config,
					},
					{
						ResourceName:      "plain_workflow.test",
						ImportState:       true,
						ImportStateVerify: true,
						// steps and start_step are rekeyed on import, and trigger cannot be
						// compared literally at all — see checkImportedTriggerIs. Everything
						// ignored here is asserted by the checks below instead.
						ImportStateVerifyIgnore: []string{"steps", "start_step", "trigger"},
						ImportStateCheck: composeImportStateChecks(
							checkImportedStepsKeyedByID,
							// ImportStateVerify already compares published against the
							// pre-import state, but only this says which value it should
							// have been — a bool that came back wrong in both places would
							// otherwise pass.
							checkImportedAttrIs("published", fmt.Sprintf("%t", tt.published)),
							checkImportedTriggerIs(`{"type":"events","events":["thread.thread_created"]}`),
							checkImportedPayloads(
								`{"version":1,"type":"priority_equals","priorities":[0]}`,
								`{"version":1,"type":"set_priority","priority":0}`,
							),
						),
					},
				},
			})
		})
	}
}

// TestAccWorkflow_importThenRekey covers what import.sh tells practitioners to
// do next: rename the opaque, ID-shaped step keys an import leaves behind.
//
// terraform-plugin-testing cannot express "apply, import the same address, then
// apply": an ImportState step with ImportStatePersist after an apply of the same
// resource fails with "Resource already managed by Terraform", and
// ImportStatePersist is rejected outright alongside plannable import blocks. So
// the first step stands in for the imported state by applying a config whose
// step keys are already ID-shaped strings, which is exactly the shape an import
// produces.
func TestAccWorkflow_importThenRekey(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-rekey")
	var beforeID, publishedAt string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// Stands in for the imported state: opaque, ID-shaped keys.
				Config: rekeyedConfig(name, true, "wfs_01aaaaaaaaaaaaaaaaaaaaaaaa", "wfs_01bbbbbbbbbbbbbbbbbbbbbbbb"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					captureID("plain_workflow.test", &beforeID),
					testAccCaptureWorkflowPublishedAt("plain_workflow.test", &publishedAt),
				),
			},
			{
				// The documented "rename the keys afterwards" apply.
				Config: rekeyedConfig(name, true, "is_urgent", "raise"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "start_step", "is_urgent"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_true", "raise"),
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					// Half of what import.sh promises: the workflow itself survives a
					// rekey, it is not replaced.
					checkIDUnchanged("plain_workflow.test", &beforeID),
					// The other half, and the point of this test. graphChanged cannot
					// tell a key rename from a delete-plus-add — Plain never sees the
					// keys — so the rename is structural, and the provider drops a
					// published workflow to a draft and republishes it. That brief
					// outage is what import.sh now warns about, and publishedAt is the
					// only evidence of it: the published attribute reads true either
					// way. If someone ever makes the rekey non-structural, this check
					// fails and the docs have to be corrected with it.
					testAccCheckWorkflowPublishedAtMoved("plain_workflow.test", &publishedAt),
				),
			},
			{
				// The rekey must settle: no perpetual diff from the new keys.
				Config:   rekeyedConfig(name, true, "is_urgent", "raise"),
				PlanOnly: true,
			},
		},
	})
}

// captureID records the workflow's Plain ID so a later step can assert it did
// not move.
func captureID(name string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		*into = id
		return nil
	}
}

// checkIDUnchanged asserts the resource was updated in place rather than
// replaced, which a changed Plain ID would give away.
func checkIDUnchanged(name string, want *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}
		if id != *want {
			return fmt.Errorf("%s was replaced: its ID moved from %s to %s. "+
				"Renaming step keys must update the existing workflow", name, *want, id)
		}

		return nil
	}
}

// checkImportedAttrIs asserts an imported attribute has an expected literal
// value. Only for attributes that compare as raw strings — a
// jsontypes.Normalized attribute needs a semantic check instead.
func checkImportedAttrIs(attr, want string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("expected one imported instance, got %d", len(states))
		}

		if got := states[0].Attributes[attr]; got != want {
			return fmt.Errorf("imported %s = %q, want %q", attr, got, want)
		}

		return nil
	}
}

func composeImportStateChecks(checks ...resource.ImportStateCheckFunc) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		for _, check := range checks {
			if err := check(states); err != nil {
				return err
			}
		}

		return nil
	}
}

// checkImportedTriggerIs asserts the imported trigger is semantically the
// expected JSON.
//
// ImportStateVerify cannot do this itself: it compares state attributes as raw
// strings. jsonencode() sorts object keys, so config produces one byte sequence
// and Plain returns another, and after an apply the framework's semantic
// equality keeps the config-shaped string in state precisely so the difference
// never surfaces as drift. An import has no prior value to keep, so Plain's
// shape lands instead and a literal comparison fails on two equivalent values.
//
// The attribute is therefore excluded from ImportStateVerify and checked here
// on the JSON rather than on the bytes. Any new jsontypes.Normalized attribute
// needs the same treatment.
func checkImportedTriggerIs(want string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("expected one imported instance, got %d", len(states))
		}

		got := states[0].Attributes["trigger"]
		equal, err := jsonEquivalent(got, want)
		if err != nil {
			return fmt.Errorf("imported trigger: %w", err)
		}
		if !equal {
			return fmt.Errorf("imported trigger is %s, want something equivalent to %s", got, want)
		}

		return nil
	}
}

// checkImportedPayloads asserts the imported steps carry the expected payloads.
//
// steps is excluded from ImportStateVerify wholesale because the keys change, so
// without this the payloads would not be verified at all. Order is not
// meaningful — the steps come back under Plain's IDs — so this matches on the
// set.
func checkImportedPayloads(want ...string) resource.ImportStateCheckFunc {
	return func(states []*terraform.InstanceState) error {
		if len(states) != 1 {
			return fmt.Errorf("expected one imported instance, got %d", len(states))
		}

		var got []string
		for key, value := range states[0].Attributes {
			if strings.HasPrefix(key, "steps.") && strings.HasSuffix(key, ".payload") {
				got = append(got, value)
			}
		}

		if len(got) != len(want) {
			return fmt.Errorf("found %d imported step payloads, want %d", len(got), len(want))
		}

		for _, wanted := range want {
			found := false
			for _, candidate := range got {
				equal, err := jsonEquivalent(candidate, wanted)
				if err != nil {
					return fmt.Errorf("imported step payload: %w", err)
				}
				if equal {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("no imported step has a payload equivalent to %s; got %v", wanted, got)
			}
		}

		return nil
	}
}

func jsonEquivalent(a, b string) (bool, error) {
	var parsedA, parsedB any
	if err := json.Unmarshal([]byte(a), &parsedA); err != nil {
		return false, fmt.Errorf("%q is not valid JSON: %w", a, err)
	}
	if err := json.Unmarshal([]byte(b), &parsedB); err != nil {
		return false, fmt.Errorf("%q is not valid JSON: %w", b, err)
	}

	return reflect.DeepEqual(parsedA, parsedB), nil
}

// checkImportedStepsKeyedByID asserts every imported step key is the step's own
// Plain ID, and that start_step was rewritten to match.
func checkImportedStepsKeyedByID(states []*terraform.InstanceState) error {
	if len(states) != 1 {
		return fmt.Errorf("expected one imported instance, got %d", len(states))
	}
	attrs := states[0].Attributes

	if got := attrs["steps.%"]; got != "2" {
		return fmt.Errorf("imported steps.%% = %q, want 2", got)
	}

	seen := 0
	for key, value := range attrs {
		// steps.<key>.id — the key segment is what we are checking.
		if !strings.HasPrefix(key, "steps.") || !strings.HasSuffix(key, ".id") {
			continue
		}
		stepKey := strings.TrimSuffix(strings.TrimPrefix(key, "steps."), ".id")
		if stepKey != value {
			return fmt.Errorf("imported step is keyed %q but its id is %q; "+
				"imported steps are expected to be keyed by Plain's step ID", stepKey, value)
		}
		seen++
	}
	if seen != 2 {
		return fmt.Errorf("found %d imported step ids, want 2", seen)
	}

	start := attrs["start_step"]
	if !strings.HasPrefix(start, stepIDPrefix) {
		return fmt.Errorf("imported start_step = %q, want a %s step ID", start, stepIDPrefix)
	}
	if _, ok := attrs["steps."+start+".id"]; !ok {
		return fmt.Errorf("imported start_step %q does not name one of the imported steps", start)
	}

	return nil
}

// TestAccWorkflow_publishLifecycle is the test the provider most needs.
//
// Plain refuses to restructure a published workflow, so the provider unpublishes
// and republishes around structural changes — but doing that for every edit
// would take live support automation offline on a routine payload tweak. This
// asserts the line graphChanged draws, in both directions, against the real API.
func TestAccWorkflow_publishLifecycle(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-publish")
	var publishedAt, afterPayloadEdit, afterCosmeticEdit string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				Config: workflowConfig(name, true, 0, "raise", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					testAccCaptureWorkflowPublishedAt("plain_workflow.test", &publishedAt),
				),
			},
			{
				// Payload-only edit: the graph is untouched, so the workflow must
				// stay live for the whole apply.
				Config: workflowConfig(name, true, 1, "raise", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					testAccCheckWorkflowPublishedAtUnchanged("plain_workflow.test", &publishedAt),
					testAccCaptureWorkflowPublishedAt("plain_workflow.test", &afterPayloadEdit),
				),
			},
			{
				// Cosmetic edit: the CONDITION step's name and both canvas
				// coordinates change while the graph stays exactly as it was.
				//
				// stepsContentChanged watches all three, so the write does go out;
				// graphChanged ignores all three, so it has to go out without taking
				// the workflow offline first. The payload-only step above proves that
				// for one of the four fields stepsContentChanged compares — this
				// proves it for the other three, which is where a widened
				// graphChanged would most plausibly land next. Renaming a step or
				// dragging a box on the canvas must never pause live automation.
				Config: workflowConfigWith(workflowOptions{
					name:      name,
					published: true,
					priority:  1,
					onTrue:    "raise",
					condName:  "Is the thread urgent enough?",
					condPos:   &canvasPos{x: 220, y: 80},
					actionPos: &canvasPos{x: 460, y: 80},
				}),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.name", "Is the thread urgent enough?"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_x", "220"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.position_y", "80"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_x", "460"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.position_y", "80"),
					testAccCheckWorkflowPublishedAtUnchanged("plain_workflow.test", &afterPayloadEdit),
					testAccCaptureWorkflowPublishedAt("plain_workflow.test", &afterCosmeticEdit),
				),
			},
			{
				// Structural edit: the branch moves from on_true to on_false. This
				// would fail outright without the unpublish/republish sequencing,
				// so the step passing at all is half the assertion.
				//
				// The name and coordinates revert here alongside the rewire, which
				// does not muddy the assertion: the step above has already shown that
				// those three on their own leave publishedAt where it is, so the
				// branch move is the only thing that can have caused a republish.
				Config: workflowConfig(name, true, 1, "", "raise"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_false", "raise"),
					resource.TestCheckNoResourceAttr("plain_workflow.test", "steps.is_urgent.on_true"),
					testAccCheckWorkflowPublishedAtMoved("plain_workflow.test", &afterCosmeticEdit),
				),
			},
			{
				// And back to a draft.
				Config: workflowConfig(name, false, 1, "", "raise"),
				Check:  resource.TestCheckResourceAttr("plain_workflow.test", "published", "false"),
			},
		},
	})
}

// TestAccWorkflow_stepsAddedAndRemoved covers reconciliation in both
// directions. bulkUpsertWorkflowSteps deletes anything absent from the list, so
// dropping a step from config has to actually remove it from Plain.
func TestAccWorkflow_stepsAddedAndRemoved(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-steps")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				Config: workflowConfig(name, false, 0, "raise", ""),
				Check:  resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
			},
			{
				// Add a WAIT step on the false branch.
				Config: workflowThreeStepConfig(name, false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "3"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.hold.type", "WAIT"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_false", "hold"),
				),
			},
			{
				// Remove it again. The step must be gone from Plain, not orphaned.
				Config: workflowConfig(name, false, 0, "raise", ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckNoResourceAttr("plain_workflow.test", "steps.hold.type"),
				),
			},
			{
				Config:   workflowConfig(name, false, 0, "raise", ""),
				PlanOnly: true,
			},
		},
	})
}

// TestAccWorkflow_invalidGraph checks the config-time validation fires before
// anything is sent to Plain — the failure a practitioner should see is a
// diagnostic naming the attribute, not a half-created workflow.
func TestAccWorkflow_invalidGraph(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      workflowConfig(acctest.RandomWithPrefix("tf-acc-workflow-bad"), false, 0, "nonexistent", ""),
				ExpectError: regexp.MustCompile(`Transition target does not match any step`),
				PlanOnly:    true,
			},
		},
	})
}
