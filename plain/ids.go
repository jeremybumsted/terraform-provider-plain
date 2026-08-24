package plain

import "github.com/oklog/ulid/v2"

// stepIDPrefix is the type prefix Plain uses for workflow step IDs.
const stepIDPrefix = "wfs_"

// newStepID mints an ID for a step that does not exist yet.
//
// bulkUpsertWorkflowSteps honours client-supplied step IDs: an ID that does not
// already exist in the workflow is created with exactly that value. Generating
// them here means a step's transitions can reference other new steps in the same
// mutation, so the whole graph is written in one call.
//
// Plain's IDs are a type prefix plus a ULID, and it accepts any well-formed
// value in that shape.
func newStepID() string {
	return stepIDPrefix + ulid.Make().String()
}
