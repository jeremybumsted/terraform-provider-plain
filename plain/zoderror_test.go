package plain

import (
	"strings"
	"testing"
)

// wrongFieldsError is the shape Plain returned for a `priority_equals` condition
// carrying `priority: 0` instead of `priorities: [0]`. Trimmed to three union
// branches; the real payload had roughly thirty, which is the whole problem.
//
// Note the third branch has no invalid_literal on `type`: that is the branch
// whose discriminator matched, and the only one worth showing.
const wrongFieldsError = `[
  {
    "code": "invalid_union",
    "unionErrors": [
      {
        "issues": [
          {"received": "priority_equals", "code": "invalid_literal",
           "expected": "company_equals", "path": ["type"],
           "message": "Invalid literal value, expected \"company_equals\""},
          {"code": "invalid_type", "expected": "array", "received": "undefined",
           "path": ["companyIds"], "message": "Required"}
        ],
        "name": "ZodError"
      },
      {
        "issues": [
          {"received": "priority_equals", "code": "invalid_literal",
           "expected": "contains_label", "path": ["type"],
           "message": "Invalid literal value, expected \"contains_label\""},
          {"code": "invalid_type", "expected": "array", "received": "undefined",
           "path": ["labelTypeIds"], "message": "Required"}
        ],
        "name": "ZodError"
      },
      {
        "issues": [
          {"code": "invalid_type", "expected": "array", "received": "undefined",
           "path": ["priorities"], "message": "Required"}
        ],
        "name": "ZodError"
      }
    ],
    "path": [],
    "message": "Invalid input"
  }
]`

// unknownTypeError is what an entirely bogus payload type produces: every
// branch rejects the discriminator, so the useful answer is the list of types.
const unknownTypeError = `[
  {
    "code": "invalid_union",
    "unionErrors": [
      {"issues": [{"received": "nonsense", "code": "invalid_literal",
        "expected": "contains_label", "path": ["type"],
        "message": "Invalid literal value, expected \"contains_label\""}],
       "name": "ZodError"},
      {"issues": [{"received": "nonsense", "code": "invalid_literal",
        "expected": "assigned_to", "path": ["type"],
        "message": "Invalid literal value, expected \"assigned_to\""}],
       "name": "ZodError"}
    ],
    "path": [],
    "message": "Invalid input"
  }
]`

func TestSummarizeValidationErrorPicksTheMatchingBranch(t *testing.T) {
	got, ok := summarizeValidationError(wrongFieldsError)
	if !ok {
		t.Fatal("summarizeValidationError() returned ok=false, want a summary")
	}

	if !strings.Contains(got, "priorities") {
		t.Errorf("summary does not mention the offending field:\n%s", got)
	}

	// The whole point is suppressing the branches that only failed because the
	// discriminator did not match.
	for _, noise := range []string{"companyIds", "labelTypeIds", "company_equals", "contains_label"} {
		if strings.Contains(got, noise) {
			t.Errorf("summary leaked non-matching branch %q:\n%s", noise, got)
		}
	}

	if lines := strings.Count(got, "\n"); lines > 6 {
		t.Errorf("summary is %d lines, want something a human reads at a glance:\n%s", lines, got)
	}
}

func TestSummarizeValidationErrorListsTypesWhenNoneMatch(t *testing.T) {
	got, ok := summarizeValidationError(unknownTypeError)
	if !ok {
		t.Fatal("summarizeValidationError() returned ok=false, want a summary")
	}

	if !strings.Contains(got, `"nonsense"`) {
		t.Errorf("summary does not name the offending type:\n%s", got)
	}
	for _, want := range []string{"contains_label", "assigned_to"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary omits valid type %q:\n%s", want, got)
		}
	}
}

func TestSummarizeValidationErrorIgnoresProse(t *testing.T) {
	// Most Plain errors are ordinary sentences and must pass through untouched.
	for _, msg := range []string{
		"There was a validation error.",
		"",
		"not json {",
	} {
		if got, ok := summarizeValidationError(msg); ok {
			t.Errorf("summarizeValidationError(%q) = %q, want passthrough", msg, got)
		}
	}
}
