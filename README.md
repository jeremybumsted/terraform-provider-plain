# Terraform Provider for Plain

Manage [Plain](https://www.plain.com) workspace configuration as code.

> **Status: early development.** Scope is workflows only. `plain_workflow` is
> implemented and verified against a live workspace; it has no acceptance test
> suite yet and `ImportState` is unexercised. See [AGENTS.md](./AGENTS.md) for
> design notes.

## Resources

| Resource | Status |
|---|---|
| `plain_workflow` | Trigger plus the full step graph — conditions, actions and waits |

Steps are declared in a `steps` map keyed by names you choose, and wired
together by those keys rather than by Plain's server-assigned step IDs:

```hcl
resource "plain_workflow" "triage" {
  name       = "Triage by priority"
  published  = true
  trigger    = jsonencode({ type = "events", events = ["thread.thread_created"] })
  start_step = "is_urgent"

  steps = {
    is_urgent = {
      type     = "CONDITION"
      payload  = jsonencode({ version = 1, type = "priority_equals", priorities = [0] })
      on_true  = "raise"
      on_false = "hold"
    }

    raise = {
      type    = "ACTION"
      payload = jsonencode({ version = 1, type = "set_priority", priority = 0 })
    }

    hold = {
      type    = "WAIT"
      payload = jsonencode({ duration = 60 })
      next    = "lower"
    }

    lower = {
      type    = "ACTION"
      payload = jsonencode({ version = 1, type = "set_priority", priority = 3 })
    }
  }
}
```

Plain refuses to restructure a published workflow, so when the graph changes the
provider unpublishes, applies, and republishes within the same apply. Payload-only
edits are not treated as structural and never take a live workflow offline.

## Usage

```hcl
terraform {
  required_providers {
    plain = {
      source = "jeremybumsted/plain"
    }
  }
}

provider "plain" {
  # or set PLAIN_API_KEY
  api_key = var.plain_api_key
}
```

## Development

Requires [mise](https://mise.jdx.dev).

```sh
mise install
mise run build
mise run test
mise tasks          # list everything available
```

Acceptance tests create and destroy real objects — point `PLAIN_API_KEY` at a
scratch workspace:

```sh
PLAIN_API_KEY=... mise run testacc
```

## License

MPL-2.0
