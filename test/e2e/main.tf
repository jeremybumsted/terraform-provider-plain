# Config for the end-to-end walkthrough driven by test/e2e/run.sh.
#
# This is deliberately not examples/resources/plain_workflow/resource.tf: that
# one is rendered into the registry docs and carries a fixed, human-readable
# name. Anything created against a real workspace has to be named tf-acc* and
# randomized, so a failed run is findable by the same sweep as the acceptance
# suite.
#
# Payloads here reference nothing outside this file. Step payloads can embed
# workspace-specific IDs (label types, users, tiers) and those are not portable
# between workspaces — a walkthrough that depended on one would only ever pass
# in the workspace it was written against.

terraform {
  required_providers {
    plain = {
      source = "jeremybumsted/plain"
    }
  }
}

# Resolved from PLAIN_API_KEY in the environment.
provider "plain" {}

variable "name" {
  description = "Workflow name. Randomized and tf-acc-prefixed by run.sh."
  type        = string
}

resource "plain_workflow" "e2e" {
  name = var.name

  # Published on purpose: the publish lifecycle is the part of this provider
  # with real sequencing behind it, and a draft workflow never exercises it.
  published = true

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

      on_true  = "raise"
      on_false = "hold"
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

    hold = {
      type = "WAIT"
      name = "Hold briefly"

      payload = jsonencode({
        duration = 60
      })

      next = "lower"
    }

    lower = {
      type = "ACTION"
      name = "Lower priority"

      payload = jsonencode({
        version  = 1
        type     = "set_priority"
        priority = 3
      })
    }
  }
}

# run.sh reads this to build the import address, so the walkthrough needs no
# jq and no poking at terraform show -json.
output "workflow_id" {
  value = plain_workflow.e2e.id
}
