package plain

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// TestAccWorkflow_identity covers resource identity across the lifecycle.
//
// The framework rejects a Create, Read or Update that returns without identity
// data — "Missing Resource Identity After Create" and friends — so every apply
// here would fail outright if a lifecycle method stopped writing it. What that
// blanket check cannot tell you is whether the *right* value went in, which is
// what ExpectIdentityValueMatchesState asserts: identity.id is the workflow's
// Plain ID, not some other string that happens to be non-null.
//
// The second step is an update rather than a repeat, because Update takes its ID
// from prior state while Create takes it from the mutation response — different
// sources for the same value, and only a step that actually updates exercises
// the second one.
func TestAccWorkflow_identity(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-identity")

	identityMatchesState := statecheck.ExpectIdentityValueMatchesState("plain_workflow.test", tfjsonpath.New("id"))

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_12_0),
		},
		Steps: []resource.TestStep{
			{
				Config:            workflowConfig(name, false, 0, "raise", ""),
				ConfigStateChecks: []statecheck.StateCheck{identityMatchesState},
			},
			{
				// A payload edit: the workflow is updated in place, so the identity
				// must survive unchanged. The framework fails the step if Update
				// returns an identity different from the stored one.
				Config:            workflowConfig(name, false, 1, "raise", ""),
				ConfigStateChecks: []statecheck.StateCheck{identityMatchesState},
			},
		},
	})
}

// TestAccWorkflow_importByIdentity drives the import path an `import` block with
// an `identity` argument takes, which is a different entry point into
// ImportState than `terraform import`: req.ID is empty and the ID arrives in
// req.Identity instead.
//
// The test framework asserts the round trip for us — it fails the step if the
// prior state carries no identity values, and again if the identity in the
// planned import does not match the one that was stored.
//
// ExpectNonEmptyPlan is required and is not a wart: the imported state keys its
// steps by Plain step ID while the config keys them by local name, so the plan
// that follows an import is an in-place update. That is the documented rekeying
// from examples/resources/plain_workflow/import.sh, and
// TestAccWorkflow_importThenRekey covers what applying it does.
func TestAccWorkflow_importByIdentity(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-workflow-identity-import")
	config := workflowConfig(name, false, 0, "raise", "")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowDestroyed,
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_12_0),
		},
		Steps: []resource.TestStep{
			{
				Config: config,
			},
			{
				Config:             config,
				ResourceName:       "plain_workflow.test",
				ImportState:        true,
				ImportStateKind:    resource.ImportBlockWithResourceIdentity,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
