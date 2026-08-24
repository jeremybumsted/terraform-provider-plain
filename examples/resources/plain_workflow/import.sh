# Workflows are imported by their Plain ID.
#
# Plain does not store the local step keys used in configuration, so imported
# steps are keyed by their Plain step ID. Rename the keys to something readable
# afterwards; the next apply recreates the steps under the new keys, which is
# safe — the graph and the workflow ID are preserved.
terraform import plain_workflow.triage wf_01HXXXXXXXXXXXXXXXXXXXXXXX
