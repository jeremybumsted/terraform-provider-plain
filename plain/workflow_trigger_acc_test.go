package plain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// Coverage for updating a workflow's trigger.
//
// trigger is Required, is one of the two jsontypes.Normalized attributes — the
// class where semantic-equality bugs live — and until these tests existed the
// branch in Update that writes it had never run against the API: every other
// config helper in this package embeds one byte-identical events trigger and
// never changes it.

// triggerWorkflowConfig renders the same two-step graph the rest of the
// acceptance tests use — a CONDITION that routes urgent threads to an ACTION —
// with the trigger supplied as a raw HCL expression.
//
// It is a local copy rather than a parameter bolted onto workflowConfig because
// the point of these tests is to vary only the trigger, and threading a sixth
// argument through a helper five other tests depend on would put their configs
// at the mercy of edits made for this one. The graph shape is deliberately
// identical to workflowConfig's so a failure here is unambiguously about the
// trigger.
//
// trigger is an expression, not a value: callers pass `jsonencode({...})` text
// or a quoted JSON literal, which is what lets the same helper cover both the
// ordinary path and the key-order drift checks below.
func triggerWorkflowConfig(name string, published bool, trigger string) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %[1]q
  published = %[2]t

  trigger = %[3]s

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

      on_true = "raise"
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
  }
}
`, name, published, trigger)
}

// hclJSONLiteral renders a JSON document as an HCL quoted string, preserving
// the key order exactly as written.
//
// jsonencode() sorts object keys, so an HCL-level reorder cannot produce a
// differently-ordered document. Only a literal can, and a literal is what it
// takes to actually put jsontypes.Normalized's semantic equality under load.
func hclJSONLiteral(document string) string {
	// Go and HCL escape quotes and backslashes the same way, and none of the
	// triggers here contain a `${` interpolation sequence.
	return strconv.Quote(document)
}

// checkTriggerIs asserts the trigger attribute is semantically the expected
// JSON, comparing parsed documents rather than bytes.
//
// A literal TestCheckResourceAttr against a hand-written JSON string is brittle
// here. trigger is jsontypes.Normalized, and after an apply the framework's
// semantic equality deliberately keeps the *config-shaped* string in state
// (ValueSemanticEqualityString sets NewValue = priorValuable), so what lands in
// state is whatever byte sequence the config produced — jsonencode's sorted key
// order in some steps, a literal's own order in others. Both are correct; only
// a parsed comparison treats them as such.
//
// This is the same reasoning as checkImportedTriggerIs in
// workflow_resource_acc_test.go, which does the equivalent job for imports.
func checkTriggerIs(name, want string) resource.TestCheckFunc {
	return resource.TestCheckResourceAttrWith(name, "trigger", func(got string) error {
		var gotJSON, wantJSON any
		if err := json.Unmarshal([]byte(got), &gotJSON); err != nil {
			return fmt.Errorf("trigger in state is not valid JSON: %q: %w", got, err)
		}
		if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
			return fmt.Errorf("expected trigger is not valid JSON: %q: %w", want, err)
		}
		if !reflect.DeepEqual(gotJSON, wantJSON) {
			return fmt.Errorf("trigger is %s, want something equivalent to %s", got, want)
		}

		return nil
	})
}

// TestAccWorkflow_triggerChanges covers the trigger update path end to end on a
// published workflow with a real CONDITION + ACTION graph.
//
// The assertion that makes it worth running is publishedAt. graphChanged does
// not look at the trigger at all, so a trigger edit must go out as a plain
// updateWorkflow and must never take live automation offline the way a
// restructure does. Nothing else in the suite can catch a regression that
// widened graphChanged to include the trigger: the published attribute reads
// true either way, so the timestamp is the only evidence.
func TestAccWorkflow_triggerChanges(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-trigger")

	const (
		threadCreated = `jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })`
		messageAdded = `jsonencode({
    type   = "events"
    events = ["thread.message_added"]
  })`
		// The same document with the object's keys written the other way round.
		// jsonencode() canonicalizes key order, so this must render byte-identical
		// JSON — the step below pins that, because if HCL ever preserved authoring
		// order it would start producing a different document from the same value.
		messageAddedReordered = `jsonencode({
    events = ["thread.message_added"]
    type   = "events"
  })`
		manual = `jsonencode({
    type = "manual"
  })`
	)

	// The same document as a literal, with the keys in an order jsonencode would
	// never emit. Re-spelling it is a real change to Terraform, not a no-op — see
	// the step that applies it.
	messageAddedLiteral := hclJSONLiteral(`{"type":"events","events":["thread.message_added"]}`)

	var publishedAt string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// Published from the start: an edit to a draft could not tell us
				// anything about the publish lifecycle.
				Config: triggerWorkflowConfig(name, true, threadCreated),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					checkTriggerIs("plain_workflow.test", `{"type":"events","events":["thread.thread_created"]}`),
					testAccCaptureWorkflowPublishedAt("plain_workflow.test", &publishedAt),
				),
			},
			{
				// Trigger-only edit. The graph is untouched, so this is the branch at
				// workflow_resource_lifecycle.go:190 running on its own.
				Config: triggerWorkflowConfig(name, true, messageAdded),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkTriggerIs("plain_workflow.test", `{"type":"events","events":["thread.message_added"]}`),
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					// The point of the test. A trigger change is not structural, so the
					// workflow must have stayed live for the whole apply.
					testAccCheckWorkflowPublishedAtUnchanged("plain_workflow.test", &publishedAt),
				),
			},
			{
				// The new trigger has to settle: a normalization bug in the read-back
				// would show up here as a permanent diff rather than as a failed apply.
				Config:   triggerWorkflowConfig(name, true, messageAdded),
				PlanOnly: true,
			},
			{
				// Same value, keys written in the other order inside jsonencode.
				Config:   triggerWorkflowConfig(name, true, messageAddedReordered),
				PlanOnly: true,
			},
			{
				// Same value again, as a literal whose key order jsonencode would never
				// produce. This has to be an apply, not a PlanOnly, and the reason is
				// worth stating because it is easy to assume otherwise: trigger is
				// Required, so its planned value is the config value verbatim, and
				// Terraform core diffs that against prior state as raw text. Semantic
				// equality never gets a say — the framework applies it only to what a
				// provider returns (server_{read,create,update}resource.go), not to
				// config-versus-state during plan. So re-spelling the config is a
				// genuine change even though Plain receives an identical document.
				//
				// What jsontypes.Normalized actually buys is the other direction:
				// Plain echoes the trigger back in its own key order, and without
				// normalization every refresh would report drift. The PlanOnly step
				// that follows each apply in this test is what proves that, and it is
				// the assertion this step was originally meant to be.
				Config: triggerWorkflowConfig(name, true, messageAddedLiteral),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkTriggerIs("plain_workflow.test", `{"type":"events","events":["thread.message_added"]}`),
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					// Re-spelling the trigger is not structural either.
					testAccCheckWorkflowPublishedAtUnchanged("plain_workflow.test", &publishedAt),
				),
			},
			{
				// And the literal spelling settles: state now holds the literal's byte
				// order, Plain returns its own, and semantic equality reconciles them.
				Config:   triggerWorkflowConfig(name, true, messageAddedLiteral),
				PlanOnly: true,
			},
			{
				// A cross-type change. workflowCapabilities reports manual as
				// unrestricted — condition support, wait support, no action-type
				// allowlist — so the existing CONDITION + ACTION graph is valid under
				// it and must come through the switch untouched. The provider does
				// nothing special for a type change, and this asserts it does not
				// need to.
				Config: triggerWorkflowConfig(name, true, manual),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkTriggerIs("plain_workflow.test", `{"type":"manual"}`),
					resource.TestCheckResourceAttr("plain_workflow.test", "published", "true"),
					// The graph is the thing most at risk in a cross-type switch, so it
					// is asserted rather than assumed: same two steps, same wiring,
					// same entry point.
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckResourceAttr("plain_workflow.test", "start_step", "is_urgent"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.type", "CONDITION"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_true", "raise"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.raise.type", "ACTION"),
					// And a type change is still not structural.
					testAccCheckWorkflowPublishedAtUnchanged("plain_workflow.test", &publishedAt),
				),
			},
			{
				Config:   triggerWorkflowConfig(name, true, manual),
				PlanOnly: true,
			},
		},
	})
}

// TestAccWorkflow_triggerScheduleRejectsConditions pins what happens when a
// trigger change makes the existing graph illegal.
//
// workflowCapabilities(triggerType: SCHEDULE) reports hasConditionSupport:
// false, hasWaitSupport: false, and an allowlist of four action types. Those
// capabilities are enforced at trigger-update time, not merely advisory: Plain
// rejects updateWorkflow with an input_validation payload error naming the
// CONDITION step.
//
// The provider does not pre-validate this, and this test does not ask it to.
// validateStepGraph checks the graph against itself, with no knowledge of what
// any given trigger type permits, and duplicating Plain's capability matrix in
// the provider would go stale silently — a new allowed action type would start
// being rejected locally for no reason a practitioner could act on. So a
// practitioner sees the API's own error, which names the problem precisely.
// This test exists to make that a deliberate, asserted behaviour rather than an
// accident, and to catch it if Plain's message ever changes shape. Do not add
// client-side capability validation on the strength of it.
func TestAccWorkflow_triggerScheduleRejectsConditions(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-trigger-schedule")

	const (
		events = `jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })`
		schedule = `jsonencode({
    type = "schedule"
    cron = "0 9 * * *"
  })`
	)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// A draft. The rejection has nothing to do with the publish
				// lifecycle, and keeping the workflow unpublished stops a failed
				// update from being confused with a failed republish.
				Config: triggerWorkflowConfig(name, false, events),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.type", "CONDITION"),
					checkTriggerIs("plain_workflow.test", `{"type":"events","events":["thread.thread_created"]}`),
				),
			},
			{
				Config: triggerWorkflowConfig(name, false, schedule),
				// Matched with \s+ between words because Terraform hard-wraps
				// diagnostic detail text, so the message can arrive with a newline
				// anywhere a space was written.
				ExpectError: regexp.MustCompile(
					`Condition\s+steps\s+are\s+not\s+allowed\s+in\s+a\s+'schedule'\s+workflow`),
			},
		},
	})
}
