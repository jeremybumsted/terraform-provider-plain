# The same import addressed by ID string rather than by identity. Use this on
# Terraform 1.5 through 1.11, which support import blocks but not `identity`.

import {
  to = plain_workflow.triage
  id = "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"
}
