package plain

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Acceptance tests create and destroy real objects in a real Plain workspace.
// They run only when TF_ACC is set — `mise run testacc`. Point PLAIN_API_KEY at
// a scratch workspace, never a production one.

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"plain": providerserver.NewProtocol6WithError(New("acctest")()),
}

// providerConfig relies on PLAIN_API_KEY and PLAIN_ENDPOINT from the
// environment, which is also how a practitioner would configure it.
const providerConfig = `provider "plain" {}
`

// skipUnlessAcc guards helpers that talk to Plain before resource.Test runs.
//
// resource.Test skips on its own when TF_ACC is unset, but only once it has been
// called. Anything that reaches the API while building a test case — fetching a
// payload shape, say — runs during an ordinary `go test` and fails there.
func skipUnlessAcc(t *testing.T) {
	t.Helper()

	if os.Getenv(resource.EnvTfAcc) == "" {
		t.Skipf("acceptance test skipped: set %s to run it", resource.EnvTfAcc)
	}

	testAccPreCheck(t)
}

func testAccPreCheck(t *testing.T) {
	t.Helper()

	if os.Getenv("PLAIN_API_KEY") == "" {
		t.Fatal("PLAIN_API_KEY must be set for acceptance tests. " +
			"Point it at a scratch workspace — these tests create and destroy real objects.")
	}
}

// testAccClient builds a client the same way the provider does, for checks that
// need to look at Plain directly rather than at Terraform state.
func testAccClient() *Client {
	endpoint := os.Getenv("PLAIN_ENDPOINT")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}

	return NewClient(endpoint, os.Getenv("PLAIN_API_KEY"), "acctest")
}

// resourceID pulls a resource's Plain ID out of Terraform state.
func resourceID(s *terraform.State, name string) (string, error) {
	rs, ok := s.RootModule().Resources[name]
	if !ok {
		return "", fmt.Errorf("%s not found in state", name)
	}
	if rs.Primary.ID == "" {
		return "", fmt.Errorf("%s has no ID in state", name)
	}

	return rs.Primary.ID, nil
}

// testAccCaptureWorkflowPublishedAt records the workflow's publishedAt
// timestamp so a later step can assert whether it was republished.
//
// This is the only way to tell the difference from outside: the provider
// exposes published as a bool, so a workflow that was unpublished and
// republished mid-apply looks identical to one that stayed live the whole time.
// Plain moves publishedAt on every publish, so the timestamp is the evidence.
func testAccCaptureWorkflowPublishedAt(name string, into *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		at, err := workflowPublishedAt(id)
		if err != nil {
			return err
		}
		if at == "" {
			return fmt.Errorf("%s is not published, so there is no publishedAt to capture", name)
		}

		*into = at
		return nil
	}
}

// testAccCheckWorkflowPublishedAtUnchanged asserts the workflow was never taken
// offline since the timestamp was captured.
func testAccCheckWorkflowPublishedAtUnchanged(name string, want *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		at, err := workflowPublishedAt(id)
		if err != nil {
			return err
		}
		if at != *want {
			return fmt.Errorf("%s was republished: publishedAt moved from %s to %s. "+
				"The change should not have been treated as structural", name, *want, at)
		}

		return nil
	}
}

// testAccCheckWorkflowPublishedAtMoved asserts the workflow was republished,
// which is what a structural change is supposed to do.
func testAccCheckWorkflowPublishedAtMoved(name string, was *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id, err := resourceID(s, name)
		if err != nil {
			return err
		}

		at, err := workflowPublishedAt(id)
		if err != nil {
			return err
		}
		if at == "" {
			return fmt.Errorf("%s was left unpublished after a structural change", name)
		}
		if at == *was {
			return fmt.Errorf("%s still has publishedAt %s, so it was never unpublished. "+
				"A restructure must drop the workflow to draft and republish it", name, at)
		}

		return nil
	}
}

func workflowPublishedAt(workflowID string) (string, error) {
	resp, err := GetWorkflow(context.Background(), testAccClient(), workflowID)
	if err != nil {
		return "", fmt.Errorf("reading workflow %s: %w", workflowID, err)
	}
	if resp.Workflow == nil {
		return "", fmt.Errorf("workflow %s no longer exists", workflowID)
	}
	if resp.Workflow.PublishedAt == nil {
		return "", nil
	}

	return resp.Workflow.PublishedAt.Iso8601, nil
}

// testAccCheckWorkflowDestroyed asserts every plain_workflow in state is gone
// from Plain, so a failed destroy does not quietly leave objects behind.
func testAccCheckWorkflowDestroyed(s *terraform.State) error {
	for name, rs := range s.RootModule().Resources {
		if rs.Type != "plain_workflow" {
			continue
		}

		resp, err := GetWorkflow(context.Background(), testAccClient(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("checking %s was destroyed: %w", name, err)
		}
		if resp.Workflow != nil {
			return fmt.Errorf("%s (%s) still exists in Plain after destroy", name, rs.Primary.ID)
		}
	}

	return nil
}

func testAccCheckWorkflowRuleDestroyed(s *terraform.State) error {
	for name, rs := range s.RootModule().Resources {
		if rs.Type != "plain_workflow_rule" {
			continue
		}

		resp, err := GetWorkflowRule(context.Background(), testAccClient(), rs.Primary.ID)
		if err != nil {
			return fmt.Errorf("checking %s was destroyed: %w", name, err)
		}
		if resp.WorkflowRule != nil {
			return fmt.Errorf("%s (%s) still exists in Plain after destroy", name, rs.Primary.ID)
		}
	}

	return nil
}
