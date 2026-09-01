# Identity is the workflow's Plain ID, and nothing else — Plain mints it at
# create and never changes it.
#
# Imported steps come back keyed by their Plain step ID rather than by the local
# keys used in configuration, because Plain does not store those keys. Renaming
# them afterwards is a structural change; see the note on the `terraform import`
# command below before doing it to a published workflow.

import {
  to = plain_workflow.triage

  identity = {
    id = "wf_01HXXXXXXXXXXXXXXXXXXXXXXX"
  }
}
