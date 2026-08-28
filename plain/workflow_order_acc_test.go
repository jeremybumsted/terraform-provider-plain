package plain

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// workflowOrderConfig renders the same two-step graph the other workflow
// acceptance configs use — a priority check that raises urgent threads — with
// order either set or omitted entirely.
//
// It is separate from workflowConfig because omitting order is load-bearing
// here: the last steps of TestAccWorkflow_order need a config with no order
// attribute at all, and an int parameter cannot express "not set" without
// rendering a value.
func workflowOrderConfig(name string, order *int64) string {
	orderLine := ""
	if order != nil {
		orderLine = fmt.Sprintf("  order     = %d\n", *order)
	}

	return providerConfig + fmt.Sprintf(`
resource "plain_workflow" "test" {
  name      = %q
  published = false
%s
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
`, name, orderLine)
}

// TestAccWorkflow_order is the only coverage the order attribute has of any
// kind. Nothing else in the suite sets it, so setOrder, its create-time call
// and the update branch that sends an IntInput have never run against Plain.
//
// order is workspace-relative — Plain positions this workflow against every
// other workflow in the scratch workspace, and the randomized name does not
// isolate that — so every assertion here is on the literal value set, never on
// a position or on an ordering relative to other workflows.
//
// The contract being asserted is the provider's stated one: the value set is
// the value read back. If Plain renumbers or clamps the value instead, this
// test fails and that is a genuine finding about the resource rather than a bad
// assertion — the schema promises a settable order and the docs describe it as
// one.
func TestAccWorkflow_order(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-order")
	orderOf := func(v int64) *int64 { return &v }

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				// Create. createWorkflow has no order field, so the provider sets it
				// with a follow-up updateWorkflow — a call that has never been made
				// by a test before. Checking Plain as well as state is what proves
				// the follow-up actually went out.
				Config: workflowOrderConfig(name, orderOf(100)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "order", "100"),
					testAccCheckWorkflowOrderIs("plain_workflow.test", 100),
				),
			},
			{
				// Update. A changed order must be folded into the ordinary
				// updateWorkflow input rather than being ignored as computed drift.
				Config: workflowOrderConfig(name, orderOf(50)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "order", "50"),
					testAccCheckWorkflowOrderIs("plain_workflow.test", 50),
				),
			},
			{
				// The point of the test. order is Optional+Computed with no
				// UseStateForUnknown, so dropping it from config makes the planned
				// value Unknown; both write branches are guarded on IsUnknown and
				// skip, and read supplies the value from Plain. Removing an
				// Optional+Computed attribute must therefore be a no-op that keeps
				// the last value — not a reset to zero, and not drift. That contract
				// is in plain-api-conventions.md and has never been proven for this
				// attribute.
				Config: workflowOrderConfig(name, nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow.test", "order", "50"),
					testAccCheckWorkflowOrderIs("plain_workflow.test", 50),
				),
			},
			{
				// And the removal must settle: an order-less config must not produce
				// a perpetual diff on the next plan.
				Config:   workflowOrderConfig(name, nil),
				PlanOnly: true,
			},
		},
	})
}
