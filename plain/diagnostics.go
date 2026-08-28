package plain

import (
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// mutationError is satisfied by every generated mutation-error type. genqlient
// gives each operation its own struct, but they all embed MutationErrorFields,
// so the methods below are promoted onto all of them.
type mutationError interface {
	Error() string
	ErrorCode() string
	RawMessage() string
	FieldErrors() map[string]string
}

// Each resource maps the Plain input fields it actually sends onto its own
// attribute names, so a field error lands on the attribute at fault. The map is
// per-resource on purpose: a shared one would attach a bulkUpsertWorkflowSteps
// error to a root attribute that does not exist.
var workflowAttributes = map[string]string{
	"name":        "name",
	"trigger":     "trigger",
	"order":       "order",
	"isPublished": "published",
	"startStepId": "start_step",
	"steps":       "steps",
}

// mutationDiagsFor is mutationDiags for the resource's own API helpers, which
// hand a Plain MutationError back as a bare error rather than as a typed
// payload field.
//
// Rendering one of those with err.Error() is a real bug, not a style choice.
// Error() is promoted from MutationErrorFields and formats the *raw* message,
// and Plain returns step-payload validation failures as a raw ZodError — tens
// of thousands of characters of union-branch noise. Putting that straight into
// a diagnostic is precisely what plain/zoderror.go exists to prevent, so any
// error that is a MutationError has to take the same route as the ones the
// mutation payloads carry.
//
// Anything else is a transport failure and prints as it is.
func mutationDiagsFor(summary string, err error) diag.Diagnostics {
	var e mutationError
	if errors.As(err, &e) {
		return mutationDiags(summary, e, workflowAttributes)
	}

	var diags diag.Diagnostics
	diags.AddError(summary, err.Error())

	return diags
}

// mutationDiags converts a Plain MutationError into diagnostics.
//
// Plain reports business failures in the mutation payload rather than the
// GraphQL errors array, and includes a stable code plus per-field detail. Both
// are worth surfacing: the code is what practitioners match on, and the fields
// point at the attribute that actually needs fixing.
func mutationDiags(summary string, e mutationError, attributes map[string]string) diag.Diagnostics {
	var diags diag.Diagnostics
	if e == nil {
		return diags
	}

	fieldErrors := e.FieldErrors()
	attached := 0

	for field, message := range fieldErrors {
		attr, ok := attributes[field]
		if !ok {
			continue
		}
		diags.AddAttributeError(path(attr), summary, message+"\n\nPlain error code: "+e.ErrorCode())
		attached++
	}

	// Anything not attributable to a specific attribute still has to be reported.
	if attached < len(fieldErrors) || attached == 0 {
		diags.AddError(summary, detail(e))
	}

	return diags
}

// detail renders the error body, collapsing Plain's raw Zod validation dumps
// into the part a practitioner can act on.
func detail(e mutationError) string {
	body := e.Error()
	if summarized, ok := summarizeValidationError(e.RawMessage()); ok {
		body = summarized + "\n\n(Plain error code: " + e.ErrorCode() + ")"
	}

	return body + "\n\nSee https://www.plain.com/docs/graphql/error-codes for what this code means. " +
		"If it is a permissions error, check the permissions on the API key rather than the key itself."
}
