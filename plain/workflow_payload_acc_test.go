package plain

import (
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// invalidPayloadConfig renders the same two-step graph the other acceptance
// tests use — a priority check that raises urgent threads — except that the
// CONDITION step's priority_equals payload carries `priorities` as a scalar
// instead of an array. That is the documented real-world mistake, and it is the
// one the Zod summarizer exists for.
//
// This helper is deliberately local rather than a parameter on workflowConfig:
// that helper renders a byte-identical valid config for every PlanOnly step in
// workflow_resource_acc_test.go, and threading a "render it broken" option
// through it would put a malformed-payload branch inside the one function whose
// output the rest of the suite compares as text.
//
// Everything here is valid at config time. The payload is well-formed JSON,
// `type` is a real discriminator value, start_step names a real key, and the
// CONDITION branches with on_true rather than next — so validateStepGraph has
// nothing to say and the rejection can only come from Plain.
func invalidPayloadConfig(name string) string {
	return providerConfig + `
resource "plain_workflow" "test" {
  name      = "` + name + `"
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

      # priorities must be an array. A scalar here is what Plain answers with an
      # 80,000-character ZodError, one branch per variant of the payload union.
      payload = jsonencode({
        version    = 1
        type       = "priority_equals"
        priorities = 0
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
`
}

// invalidPayloadSummarized matches the summary summarizeValidationError renders
// when exactly one union branch accepted the discriminator — which is this case,
// since `type` is a valid `priority_equals` and only the field shape is wrong.
// zoderror.go's matched-branch path writes a fixed header, then one "- path:
// message" line per outstanding issue, having suppressed every branch that
// failed only because its type literal did not match.
//
// The header is provider-authored text and so is the stable half of this
// assertion; `priorities` is the field Plain names. Requiring both is what
// distinguishes a summarized diagnostic from the raw ZodError, which contains
// the field name hundreds of times and the header not at all.
//
// If Plain changes its error format — a new ZodError shape, a different nesting
// of unionErrors, prose instead of JSON — the summarizer stops recognizing it,
// the diagnostic degrades to the full 84,000-character dump, and this test
// fails. That is the intent. This test exists precisely so that degradation is
// loud, rather than something a practitioner discovers the first time they get a
// payload wrong. Do not relax this regex to accommodate a raw-Zod diagnostic;
// fix the summarizer instead.
//
// The whitespace classes are not decoration: Terraform word-wraps diagnostic
// detail and prefixes each continuation line with "│ ", so any phrase long
// enough to be worth matching can be split across lines.
var invalidPayloadSummarized = regexp.MustCompile(
	`(?s)payload[\s│]+matched[\s│]+its[\s│]+type[\s│]+but[\s│]+its[\s│]+fields[\s│]+are[\s│]+not[\s│]+valid.*priorities`,
)

// TestAccWorkflow_invalidPayload pins the Zod summarizer to the live API.
//
// zoderror.go is load-bearing — without it the provider is unusable for
// authoring payloads — but its unit tests run against captured fixtures, so
// they prove the parser still handles a 2026 error, not that Plain still emits
// one. Only a real rejection proves that.
//
// This cannot be PlanOnly. The payload is valid JSON and the graph is
// well-formed, so config-time validation passes; the error only exists once
// Plain's Zod schema sees the payload, which happens during
// bulkUpsertWorkflowSteps, which happens during apply.
func TestAccWorkflow_invalidPayload(t *testing.T) {
	// createWorkflow succeeds here and only the following
	// bulkUpsertWorkflowSteps fails, so this test leaves a real half-built
	// workflow behind for the duration of the run. Create writes the ID to
	// state before returning the error precisely so Terraform can still destroy
	// it (see the saveID closure in workflow_resource_lifecycle.go) — so this
	// needs a randomized name and a CheckDestroy exactly like the tests that
	// apply successfully. ExpectError is not a promise that nothing was created.
	name := acctest.RandomWithPrefix("tf-acc-workflow-badpayload")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		Steps: []resource.TestStep{
			{
				Config:      invalidPayloadConfig(name),
				ExpectError: invalidPayloadSummarized,
			},
		},
	})
}
