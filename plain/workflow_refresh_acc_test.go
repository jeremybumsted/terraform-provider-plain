package plain

import (
	"context"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Coverage for what happens when a workflow under management disappears from
// Plain — someone deletes it in the UI while Terraform still has it in state.
//
// Read has a branch for exactly this (workflow_resource_lifecycle.go): a
// workflow that no longer exists is removed from state with RemoveResource
// rather than reported as an error, so the next plan offers to recreate it.
// That is the difference between a recoverable "Terraform will create" and a
// dead end where every plan fails on an object that is already gone. It had
// never been exercised.

// outOfBandWorkflowConfig renders the same two-step draft the rest of the
// workflow tests use — a priority check that raises urgent threads.
//
// A local copy on purpose. This test's subject is the resource vanishing
// underneath a fixed config, so the config has to be byte-for-byte identical
// across both steps and must not shift because a shared helper grew a
// parameter for some unrelated test.
func outOfBandWorkflowConfig(name string) string {
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

// TestAccWorkflow_deletedOutOfBand asserts that a workflow deleted outside
// Terraform comes back as a create, not as an apply error.
//
// The workflow stays a draft throughout. Nothing here is about publishing, and
// a published workflow would only add a lifecycle path that
// TestAccWorkflow_publishLifecycle already owns.
func TestAccWorkflow_deletedOutOfBand(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-oob")

	// PreConfig runs before its step and is handed no state, so the ID has to be
	// captured by an earlier step's Check and read out of this closure.
	var workflowID string

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// CheckDestroy still has something to assert here, and it passes for the
		// right reason. A PlanOnly step refreshes in memory only, so the workflow
		// is still recorded in the on-disk state that CheckDestroy is handed, and
		// the assertion it makes — gone from Plain — is true because PreConfig
		// deleted it.
		//
		// The destroy that runs just before it does refresh, which drops the
		// resource from state, so the provider's Delete may never be called at
		// all. If it is, Delete's not_found handling is what keeps the teardown
		// clean: deleting an already-deleted workflow returns HTTP 200 with a
		// not_found payload error, and Delete treats that as a success.
		CheckDestroy: testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// A workflow to pull out from under Terraform.
				Config: outOfBandWorkflowConfig(name),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "steps.%", "2"),
					captureOutOfBandWorkflowID("plain_workflow.test", &workflowID),
				),
			},
			{
				// Delete it directly through the API, the way Plain's UI would.
				PreConfig: func() {
					if err := deleteWorkflowOutOfBand(workflowID); err != nil {
						t.Fatalf("deleting the workflow out of band: %s", err)
					}
				},
				// Same config, unchanged. Without Read's RemoveResource branch the
				// refresh would fail — or worse, keep a phantom resource in state and
				// plan an update against a workflow that does not exist. With it, the
				// refresh drops the resource and the plan becomes a create, which is
				// what ExpectNonEmptyPlan asserts.
				Config:             outOfBandWorkflowConfig(name),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// captureOutOfBandWorkflowID records the workflow's Plain ID for a later step's
// PreConfig, which has no state to look it up in.
//
// Deliberately local rather than shared: this test owns its own capture so it
// keeps working if the equivalent helper elsewhere in the package changes
// shape.
func captureOutOfBandWorkflowID(name string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		*into = id
		return nil
	}
}

// deleteWorkflowOutOfBand removes the workflow through Plain's API directly,
// standing in for a practitioner deleting it in the UI.
//
// Plain reports business failures in the mutation payload rather than in the
// GraphQL errors array, so the payload error has to be checked too — a
// transport-only check would report a silent success and the test would then
// fail confusingly at the plan, on a workflow that is still there.
func deleteWorkflowOutOfBand(workflowID string) error {
	if workflowID == "" {
		return fmt.Errorf("no workflow ID was captured by the previous step")
	}

	resp, err := DeleteWorkflow(context.Background(), testAccClient(), &DeleteWorkflowInput{
		WorkflowId: workflowID,
	})
	if err != nil {
		return fmt.Errorf("deleting workflow %s: %w", workflowID, err)
	}
	if resp.DeleteWorkflow.Error != nil {
		return fmt.Errorf("deleting workflow %s: %w", workflowID, resp.DeleteWorkflow.Error)
	}

	return nil
}
