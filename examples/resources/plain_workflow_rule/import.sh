# Workflow rules are imported by their Plain ID. Unlike workflows, a rule has no
# steps, so nothing is renamed on the way in and the imported state matches the
# configuration exactly.
#
# Read the ID off the rule's page in Plain, or list them:
#
#   query { workflowRules(first: 50) { edges { node { id name } } } }
terraform import plain_workflow_rule.auto_label_billing <workflow rule id>
