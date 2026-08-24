package plain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Plain validates step and trigger payloads with Zod and returns the raw
// ZodError in MutationError.message. For a discriminated union — which is what
// every condition and action payload is — that means one branch per known type,
// each explaining why the payload is not that type. A single wrong field
// produces hundreds of lines of which one line matters.
//
// The code below finds the line that matters.

type zodError struct {
	Issues []zodIssue `json:"issues"`
}

type zodIssue struct {
	Code        string     `json:"code"`
	Expected    any        `json:"expected"`
	Received    any        `json:"received"`
	Path        []any      `json:"path"`
	Message     string     `json:"message"`
	UnionErrors []zodError `json:"unionErrors"`
}

func (i zodIssue) pathString() string {
	parts := make([]string, 0, len(i.Path))
	for _, p := range i.Path {
		parts = append(parts, fmt.Sprintf("%v", p))
	}

	return strings.Join(parts, ".")
}

// discriminatorMismatch reports whether this issue says "the type field is not
// this branch's literal" — i.e. the branch is simply the wrong variant, not a
// real complaint about the payload.
func (i zodIssue) discriminatorMismatch() (expected string, ok bool) {
	if i.Code != "invalid_literal" || i.pathString() != "type" {
		return "", false
	}

	s, isString := i.Expected.(string)
	if !isString {
		return "", false
	}

	return s, true
}

// branches flattens a nested union into one issue list per candidate variant.
func branches(issues []zodIssue) [][]zodIssue {
	var direct, unions []zodIssue
	for _, issue := range issues {
		if issue.Code == "invalid_union" && len(issue.UnionErrors) > 0 {
			unions = append(unions, issue)
			continue
		}
		direct = append(direct, issue)
	}

	if len(unions) == 0 {
		return [][]zodIssue{direct}
	}

	var out [][]zodIssue
	for _, union := range unions {
		for _, alternative := range union.UnionErrors {
			for _, sub := range branches(alternative.Issues) {
				combined := make([]zodIssue, 0, len(direct)+len(sub))
				combined = append(combined, direct...)
				combined = append(combined, sub...)
				out = append(out, combined)
			}
		}
	}

	return out
}

// summarizeValidationError turns a raw ZodError payload into something a human
// can act on. It returns false if the message is not a ZodError, in which case
// the caller should surface the original text.
func summarizeValidationError(message string) (string, bool) {
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		return "", false
	}

	var issues []zodIssue
	if err := json.Unmarshal([]byte(trimmed), &issues); err != nil {
		var single zodError
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil || len(single.Issues) == 0 {
			return "", false
		}
		issues = single.Issues
	}
	if len(issues) == 0 {
		return "", false
	}

	var (
		viable        [][]zodIssue
		knownTypes    []string
		receivedType  string
		anyMismatched bool
	)

	for _, branch := range branches(issues) {
		mismatch := false
		for _, issue := range branch {
			expected, ok := issue.discriminatorMismatch()
			if !ok {
				continue
			}
			mismatch = true
			anyMismatched = true
			knownTypes = append(knownTypes, expected)
			if s, isString := issue.Received.(string); isString && receivedType == "" {
				receivedType = s
			}
		}
		if !mismatch && len(branch) > 0 {
			viable = append(viable, branch)
		}
	}

	// Every branch rejected the discriminator: the type itself is wrong, and the
	// full set of expected literals is the useful answer.
	if len(viable) == 0 {
		if !anyMismatched {
			return "", false
		}

		sort.Strings(knownTypes)
		knownTypes = dedupe(knownTypes)

		var b strings.Builder
		if receivedType != "" {
			fmt.Fprintf(&b, "%q is not a valid type for this payload.\n\n", receivedType)
		} else {
			b.WriteString("The payload's \"type\" is not valid.\n\n")
		}
		fmt.Fprintf(&b, "Valid types are:\n  %s", strings.Join(knownTypes, "\n  "))

		return b.String(), true
	}

	// The discriminator matched at least one branch, so those branches carry the
	// real complaint. Prefer the most specific: fewest outstanding issues.
	sort.SliceStable(viable, func(a, b int) bool { return len(viable[a]) < len(viable[b]) })
	best := viable[0]

	var b strings.Builder
	b.WriteString("The payload matched its type but its fields are not valid:\n")
	for _, issue := range best {
		path := issue.pathString()
		if path == "" {
			path = "(payload)"
		}

		fmt.Fprintf(&b, "\n  - %s: %s", path, issue.Message)
		if expected, ok := issue.Expected.(string); ok && expected != "" && issue.Message == "Required" {
			fmt.Fprintf(&b, " (expected %s)", expected)
		}
	}

	return b.String(), true
}

func dedupe(in []string) []string {
	out := in[:0]
	var last string
	for i, s := range in {
		if i > 0 && s == last {
			continue
		}
		out = append(out, s)
		last = s
	}

	return out
}
