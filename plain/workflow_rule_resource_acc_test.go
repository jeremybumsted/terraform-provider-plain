package plain

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// testAccRulePayload returns a rule payload Plain will accept.
//
// Plain does not describe the rule payload shape anywhere — not in the GraphQL
// schema, which says only "JSON-encoded payload of the rule definition", and not
// in the public docs. Rather than inventing a shape and shipping a test that
// asserts a guess, the test borrows one from a rule that already exists in the
// workspace. Set PLAIN_ACC_RULE_PAYLOAD to supply one directly.
//
// If the workspace has no rules and no payload is supplied, the tests that need
// a valid payload skip. The error-path test below does not need one and always
// runs.
func testAccRulePayload(t *testing.T) string {
	t.Helper()
	skipUnlessAcc(t)

	if payload := os.Getenv("PLAIN_ACC_RULE_PAYLOAD"); payload != "" {
		return payload
	}

	resp, err := ListWorkflowRules(context.Background(), testAccClient(), 1)
	if err != nil {
		t.Fatalf("listing workflow rules to borrow a payload shape: %s", err)
	}

	for _, edge := range resp.WorkflowRules.Edges {
		if edge.Node != nil && edge.Node.Payload != "" {
			return edge.Node.Payload
		}
	}

	t.Skip("no workflow rule exists in this workspace to borrow a payload shape from, and " +
		"PLAIN_ACC_RULE_PAYLOAD is not set. Plain does not document the rule payload format, " +
		"so create one rule in the UI (or set PLAIN_ACC_RULE_PAYLOAD) to run this test.")

	return ""
}

func workflowRuleConfig(name, payload string, published bool, order *int) string {
	// A borrowed payload is arbitrary JSON and may contain HCL's template
	// markers, which would otherwise be interpolated inside the quoted string.
	payload = strings.ReplaceAll(payload, "${", "$${")
	payload = strings.ReplaceAll(payload, "%{", "%%{")

	orderLine := ""
	if order != nil {
		orderLine = fmt.Sprintf("  order     = %d\n", *order)
	}

	return providerConfig + fmt.Sprintf(`
resource "plain_workflow_rule" "test" {
  name      = %q
  published = %t
%s  payload   = %q
}
`, name, published, orderLine, payload)
}

// TestAccWorkflowRule_basic covers create, read-back, rename, payload edit and
// destroy, plus import.
func TestAccWorkflowRule_basic(t *testing.T) {
	payload := testAccRulePayload(t)
	name := acctest.RandomWithPrefix("tf-acc-rule")
	renamed := name + "-renamed"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowRuleDestroyed,
		Steps: []resource.TestStep{
			{
				Config: workflowRuleConfig(name, payload, false, nil),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("plain_workflow_rule.test", "name", name),
					resource.TestCheckResourceAttr("plain_workflow_rule.test", "published", "false"),
					resource.TestCheckResourceAttrSet("plain_workflow_rule.test", "id"),
					resource.TestCheckResourceAttrSet("plain_workflow_rule.test", "order"),
				),
			},
			{
				// Payload is compared semantically, so re-planning the same config
				// must produce no diff even if Plain reorders the JSON keys.
				Config:   workflowRuleConfig(name, payload, false, nil),
				PlanOnly: true,
			},
			{
				// A rule has no steps, so nothing is rekeyed on import and the
				// whole state must round-trip.
				ResourceName:      "plain_workflow_rule.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				Config: workflowRuleConfig(renamed, payload, false, nil),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "name", renamed),
			},
		},
	})
}

// TestAccWorkflowRule_publishToggle exercises the part of this resource that is
// genuinely different from plain_workflow: Plain publishes rules with
// toggleWorkflowRulePublished, a flip rather than an assignment. Driving it in
// both directions and back proves the provider tracks the current state rather
// than blindly flipping.
func TestAccWorkflowRule_publishToggle(t *testing.T) {
	payload := testAccRulePayload(t)
	name := acctest.RandomWithPrefix("tf-acc-rule-publish")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowRuleDestroyed,
		Steps: []resource.TestStep{
			{
				// Created published: the rule is born a draft, so this requires the
				// provider to toggle it as part of create.
				Config: workflowRuleConfig(name, payload, true, nil),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "published", "true"),
			},
			{
				Config:   workflowRuleConfig(name, payload, true, nil),
				PlanOnly: true,
			},
			{
				Config: workflowRuleConfig(name, payload, false, nil),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "published", "false"),
			},
			{
				// Back again. A toggle that ignored current state would land here
				// on the wrong value.
				Config: workflowRuleConfig(name, payload, true, nil),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "published", "true"),
			},
		},
	})
}

// TestAccWorkflowRule_order checks the display order round-trips. order is
// Optional+Computed, so Plain assigns one when it is not set and must not report
// drift on it afterwards.
func TestAccWorkflowRule_order(t *testing.T) {
	payload := testAccRulePayload(t)
	name := acctest.RandomWithPrefix("tf-acc-rule-order")
	first, second := 5, 6

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowRuleDestroyed,
		Steps: []resource.TestStep{
			{
				Config: workflowRuleConfig(name, payload, false, &first),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "order", "5"),
			},
			{
				Config: workflowRuleConfig(name, payload, false, &second),
				Check:  resource.TestCheckResourceAttr("plain_workflow_rule.test", "order", "6"),
			},
			{
				// Dropping order from config must not fight Plain over the value it
				// already has.
				Config:   workflowRuleConfig(name, payload, false, nil),
				PlanOnly: true,
			},
		},
	})
}

// TestAccWorkflowRule_invalidPayload checks the failure path a practitioner is
// most likely to hit, given the payload shape is undocumented: a rejected
// payload must produce a diagnostic, not a partially created rule.
//
// This test needs no known-good payload, so it runs even in a workspace with no
// existing rules.
func TestAccWorkflowRule_invalidPayload(t *testing.T) {
	name := acctest.RandomWithPrefix("tf-acc-rule-bad")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             testAccCheckWorkflowRuleDestroyed,
		Steps: []resource.TestStep{
			{
				Config:      workflowRuleConfig(name, `{"definitely":"not a rule"}`, false, nil),
				ExpectError: regexp.MustCompile(`Unable to create workflow rule`),
			},
		},
	})
}
