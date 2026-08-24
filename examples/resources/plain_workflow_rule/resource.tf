# Workflow rules are Plain's older automation model: one named JSON rule
# definition, with no steps and no graph. Use `plain_workflow` for new
# automation — this resource exists to manage rules that already exist.
#
# Plain does not publish a schema for rule payloads. Its GraphQL API describes
# the field only as "JSON-encoded payload of the rule definition", so rather
# than guessing at the shape, keep the payload in a file alongside your
# configuration and read it in. To get a starting point, fetch an existing
# rule's payload from the API:
#
#   query { workflowRule(workflowRuleId: "wfr_...") { payload } }
#
# Payloads embed workspace-specific IDs, so a payload copied from another
# workspace references IDs that do not exist in this one.

resource "plain_workflow_rule" "auto_label_billing" {
  name      = "Auto-label billing threads"
  published = true

  payload = file("${path.module}/rules/auto-label-billing.json")
}

# A rule kept as a draft while it is being written. Unpublished rules never
# fire, so this is the safe way to stage a change.
resource "plain_workflow_rule" "escalation_draft" {
  name      = "Escalate enterprise threads"
  published = false
  order     = 10

  payload = file("${path.module}/rules/escalation.json")
}
