package plain

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"

	"github.com/Khan/genqlient/graphql"
)

// Plain's core-api intermittently serves HTTP 500 for minutes at a time. A
// single transient 5xx should not fail an apply, so retry — but only for
// operations where a lost response cannot have changed the workspace.
//
// retryableOps is that set, by genqlient operation name. Read queries are safe
// by definition. DeleteWorkflow is safe because Delete already treats
// not_found as success, so replaying a delete that actually landed still
// succeeds.
//
// Every other mutation is deliberately absent. A 500 from CreateWorkflow,
// UpdateWorkflow or BulkUpsertWorkflowSteps may mean the write applied and only
// the response was lost; retrying would duplicate a workflow or re-run a graph
// replace. Do not add them.
var retryableOps = map[string]bool{
	"GetWorkflow":    true,
	"DeleteWorkflow": true,
}

// retryPolicy bounds a retry sequence. The whole sequence — every attempt plus
// every backoff — must fit inside budget, which mirrors the HTTP client's
// timeout so a retrying call takes no longer in the worst case than a single
// non-retrying one did.
type retryPolicy struct {
	maxAttempts int
	baseDelay   time.Duration
	maxDelay    time.Duration
	budget      time.Duration
}

var defaultRetryPolicy = retryPolicy{
	maxAttempts: 4,
	baseDelay:   500 * time.Millisecond,
	maxDelay:    8 * time.Second,
	budget:      clientTimeout,
}

// backoff returns the wait before the given retry (1-based), using full jitter:
// a uniform draw from [0, exponential cap). Jitter matters more than the curve
// here — Terraform destroys resources concurrently, and unjittered backoff
// would march every goroutine into the same retry instant.
func (p retryPolicy) backoff(retry int) time.Duration {
	window := p.baseDelay << (retry - 1)
	if window > p.maxDelay || window <= 0 {
		window = p.maxDelay
	}

	return rand.N(window)
}

// retryClient wraps a graphql.Client and replays failed requests for the
// operations in retryableOps.
//
// This wraps the GraphQL client rather than the http.RoundTripper for two
// reasons. The transport sees only an http.Request, so scoping there would mean
// parsing the operation name back out of the JSON body and buffering that body
// to make it replayable. At this layer the operation name arrives as a struct
// field, and genqlient's client marshals a fresh request on every MakeRequest,
// so replay costs nothing. It also keeps the policy in one place instead of
// spread across every call site, which the call sites cannot express anyway —
// they hold a graphql.Client, not the transport.
type retryClient struct {
	next   graphql.Client
	policy retryPolicy
}

func (c *retryClient) MakeRequest(ctx context.Context, req *graphql.Request, resp *graphql.Response) error {
	if !retryableOps[req.OpName] {
		return c.next.MakeRequest(ctx, req, resp)
	}

	ctx, cancel := context.WithTimeout(ctx, c.policy.budget)
	defer cancel()

	var err error

	for attempt := 1; ; attempt++ {
		err = c.next.MakeRequest(ctx, req, resp)
		if err == nil || !isRetryable(err) || attempt >= c.policy.maxAttempts {
			return err
		}

		// Give up rather than sleep past the budget: a wait we cannot finish
		// buys nothing, and returning the 5xx beats returning a deadline error.
		delay := c.policy.backoff(attempt)
		if deadline, ok := ctx.Deadline(); ok && time.Now().Add(delay).After(deadline) {
			return err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
}

// isRetryable reports whether an error from genqlient is worth another attempt.
//
// Only HTTP 5xx and genuine transport failures qualify, and the default is to
// not retry. Plain reports business errors in the response payload with HTTP
// 200, so those never reach here at all — genqlient returns a nil error and
// MakeRequest hands the payload straight back to the caller. GraphQL errors (a
// gqlerror.List on a 200), 4xx, and malformed response bodies all fall through
// to false: none of them will fix themselves on a second try.
func isRetryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var httpErr *graphql.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode >= http.StatusInternalServerError
	}

	// net/http wraps every failure to complete a round trip — connection reset,
	// TLS handshake, DNS, per-attempt timeout — in *url.Error. That makes it a
	// precise marker for "the request did not get a response", as distinct from
	// a response we got but could not decode.
	var urlErr *url.Error

	return errors.As(err, &urlErr)
}
