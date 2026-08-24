# Raise urgent threads immediately; hold everything else briefly, then lower it.
#
# Steps are keyed by names you choose. Those keys are local to this resource —
# `start_step`, `on_true`, `on_false` and `next` refer to them, and the provider
# resolves them to Plain's step IDs for you.

resource "plain_workflow" "triage" {
  name      = "Triage by priority"
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

      # No `next`: this branch ends here.
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

# Payloads that reference other objects must be built from references, never
# from IDs pasted out of another workspace. A stale ID does not error — Plain
# accepts the step and it silently does nothing.
#
#   payload = jsonencode({
#     version      = 1
#     type         = "apply_labels"
#     labelTypeIds = [plain_label_type.escalated.id]
#   })
#
# Plain does not publish a field reference for payload types. If you get one
# wrong, the provider reports the offending field, and if the `type` itself is
# unrecognised it lists every type Plain will accept in that position.
