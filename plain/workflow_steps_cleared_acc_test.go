package plain

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Coverage for emptying a workflow of its steps.
//
// syncSteps has a dedicated zero-step branch that clears the steps and the
// entry point together (workflow_resource_crud.go), and read has a matching
// MapNull branch. Delete-by-omission was hand-verified when the resource landed
// and never automated, so neither branch had ever run in a test.
//
// validateStepGraph explicitly allows a workflow with no steps and no entry
// point (see the "no steps and no entry point is valid" case in
// workflow_validate_test.go), so this is a supported configuration rather than
// an edge case being smuggled in.

// stepsClearedTwoStepConfig renders the same two-step graph the other workflow
// tests use — a priority check that raises urgent threads — as a draft.
//
// It is deliberately a local copy rather than a call into workflowConfig: this
// test's whole subject is the transition between having this graph and having
// none, so it needs a matching "no steps" config rendered from the same
// resource block, and it must not break if the shared helper grows parameters
// for an unrelated test.
func stepsClearedTwoStepConfig(name string) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %q
  published = false

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
`, name)
}

// stepsClearedNoStepsConfig is the same workflow with both steps and start_step
// omitted.
//
// Both have to go together. validateStepGraph requires start_step to name a
// real key, so leaving the entry point behind with no steps to point at is a
// config error, not the case under test.
func stepsClearedNoStepsConfig(name string) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %q
  published = false

  trigger = jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })
}
`, name)
}

// stepsClearedEmptyMapConfig writes an explicit empty map instead of omitting
// the attribute. See TestAccWorkflow_stepsClearedEmptyMap for why that is a
// separate case.
func stepsClearedEmptyMapConfig(name string) string {
	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %q
  published = false

  trigger = jsonencode({
    type   = "events"
    events = ["thread.thread_created"]
  })

  steps = {}
}
`, name)
}

// TestAccWorkflow_stepsCleared covers removing every step from an existing
// workflow by omitting the attribute, and then rebuilding the graph.
//
// The workflow stays a draft for the whole test, on purpose. Whether Plain will
// publish a workflow with no startStepId is unknown, and if it refuses, that
// belongs in validateStepGraph as a config-time rule rather than being
// discovered at apply time. Publishing a stepless workflow is not in scope
// here, and this test must not be the thing that finds out.
func TestAccWorkflow_stepsCleared(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-steps-cleared")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// A graph to clear.
				Config: stepsClearedTwoStepConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckResourceAttr("plain_workflow.test", "start_step", "is_urgent"),
				),
			},
			{
				// Delete by omission: syncSteps' zero-step branch.
				Config: stepsClearedNoStepsConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("plain_workflow.test", "steps.%"),
					resource.TestCheckNoResourceAttr("plain_workflow.test", "start_step"),
					// State alone cannot prove the entry point was cleared — start_step
					// reads back null whether Plain dropped startStepId or the provider
					// simply stopped tracking it, and clearing it is half of what the
					// branch under test does. Only Plain's own view distinguishes the
					// two, and a workflow left with a dangling startStepId pointing at a
					// deleted step would be a real corruption.
					testAccCheckWorkflowHasNoSteps("plain_workflow.test"),
				),
			},
			{
				// The cleared state must settle. This is where read's MapNull branch
				// has to agree with a config that omits the attribute: if read
				// returned an empty map instead, or resurrected start_step, this plan
				// would be non-empty.
				Config:   stepsClearedNoStepsConfig(name),
				PlanOnly: true,
			},
			{
				// Clearing must not be one-way. Every step ID from the first apply is
				// gone from state, so this rebuild goes out with freshly minted IDs
				// and a startStepId that was previously null — the same path a brand
				// new workflow takes, but against a workflow that already exists.
				Config: stepsClearedTwoStepConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					resource.TestCheckResourceAttr("plain_workflow.test", "start_step", "is_urgent"),
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.is_urgent.on_true", "raise"),
					resource.TestCheckResourceAttrWith("plain_workflow.test", "steps.is_urgent.id", hasStepIDPrefix),
					resource.TestCheckResourceAttrWith("plain_workflow.test", "steps.raise.id", hasStepIDPrefix),
				),
			},
		},
	})
}

// TestAccWorkflow_stepsClearedEmptyMap covers `steps = {}` rather than omitting
// the attribute.
//
// This is a probe of a suspected bug, kept deliberately separate from
// TestAccWorkflow_stepsCleared so that if it fails it identifies the bug in
// isolation instead of taking the main test down with it.
//
// The suspicion: read returns types.MapNull whenever the workflow has no steps,
// collapsing "empty" and "unset" into one value. A config that writes an empty
// map therefore plans {} and gets null back, which Terraform rejects as
// "Provider produced inconsistent result after apply". Practitioners do write
// `steps = {}` — it is the obvious way to say "no steps" when the attribute was
// there a moment ago — so the behaviour asserted below is the behaviour we
// want: an empty map applies and settles cleanly, indistinguishable in effect
// from omitting the attribute.
//
// If this fails against the live API, the fix is in read — preserve
// empty-versus-null rather than collapsing both to null — and that is a
// decision for a human to make. Do not "fix" it by weakening this test to
// assert the current behaviour.
func TestAccWorkflow_stepsClearedEmptyMap(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-steps-empty-map")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				Config: stepsClearedTwoStepConfig(name),
				Check: resource.TestCheckResourceAttr(
					"plain_workflow.test", "steps.%", "2"),
			},
			{
				// The apply is the assertion: if read collapses {} to null, the
				// framework fails this step with "Provider produced inconsistent
				// result after apply" before any check below runs.
				Config: stepsClearedEmptyMapConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					testAccCheckWorkflowHasNoSteps("plain_workflow.test"),
				),
			},
			{
				// And it has to hold across a refresh, not just survive the apply.
				Config:   stepsClearedEmptyMapConfig(name),
				PlanOnly: true,
			},
		},
	})
}

// testAccCheckWorkflowHasNoSteps asserts Plain's own view of a cleared
// workflow: it still exists, it has no steps, and its startStepId is null.
//
// Modelled on workflowPublishedAt in acc_test.go, and for the same reason —
// Terraform state is populated by read, so checking state against the config
// only proves the provider agrees with itself. Reading Plain back is what
// proves the mutation actually went out and cleared both halves.
func testAccCheckWorkflowHasNoSteps(name string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		resp, err := GetWorkflow(context.Background(), testAccClient(), id)
		if err != nil {
			return fmt.Errorf("reading workflow %s: %w", id, err)
		}
		if resp.Workflow == nil {
			return fmt.Errorf("workflow %s no longer exists — clearing its steps "+
				"must empty the workflow, not delete it", id)
		}
		if n := len(resp.Workflow.Steps); n != 0 {
			return fmt.Errorf("workflow %s still has %d step(s) in Plain after they "+
				"were removed from the config", id, n)
		}
		if resp.Workflow.StartStepId != nil {
			return fmt.Errorf("workflow %s still has startStepId %q in Plain. "+
				"Clearing the steps must clear the entry point with them, or the "+
				"workflow is left pointing at a step that no longer exists",
				id, *resp.Workflow.StartStepId)
		}

		return nil
	}
}

// hasStepIDPrefix asserts a step carries a Plain-shaped step ID, which is how a
// rebuilt graph shows it was actually written rather than echoed back from
// config.
func hasStepIDPrefix(v string) error {
	if !strings.HasPrefix(v, stepIDPrefix) {
		return fmt.Errorf("step id %q does not start with %q", v, stepIDPrefix)
	}

	return nil
}
