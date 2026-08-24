package plain

import (
	"fmt"
	"strings"
)

// Error renders a Plain MutationError as a Go error, including the stable error
// code so practitioners can match on it.
//
// See https://www.plain.com/docs/graphql/error-codes.
func (e *MutationErrorFields) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (code: %s)", e.Message, e.Code)

	for _, f := range e.Fields {
		fmt.Fprintf(&b, "\n  - %s: %s", f.Field, f.Message)
	}

	return b.String()
}

// FieldErrors returns the per-field errors keyed by field name, so callers can
// attach diagnostics to the attribute actually at fault.
func (e *MutationErrorFields) FieldErrors() map[string]string {
	if len(e.Fields) == 0 {
		return nil
	}

	out := make(map[string]string, len(e.Fields))
	for _, f := range e.Fields {
		out[f.Field] = f.Message
	}

	return out
}

// ErrorCode returns Plain's stable error code for this failure.
func (e *MutationErrorFields) ErrorCode() string { return e.Code }

// RawMessage returns Plain's error message, which for validation failures is a
// JSON-encoded ZodError rather than prose.
func (e *MutationErrorFields) RawMessage() string { return e.Message }
