# Workflows are imported by their Plain ID.
#
# Plain does not store the local step keys used in configuration, so imported
# steps are keyed by their Plain step ID — and start_step, next, on_true and
# on_false come back as step IDs too.
#
# Renaming those keys to something readable is a structural change: the next
# apply recreates every step under a new ID and rewires the graph. The
# workflow's own ID and the shape of the graph are preserved, but the step IDs
# are not, and on a published workflow the provider drops it to a draft,
# restructures, and republishes — so it is briefly offline during that apply,
# exactly as for any other graph change. Rename before publishing where you can.
terraform import plain_workflow.triage wf_01HXXXXXXXXXXXXXXXXXXXXXXX
