package plain

import (
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
