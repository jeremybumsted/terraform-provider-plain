package plain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Khan/genqlient/graphql"
)

// testRetryPolicy keeps the shape of the real policy — bounded attempts,
// jittered backoff, an overall budget — but with waits short enough that the
// unit suite stays fast.
var testRetryPolicy = retryPolicy{
	maxAttempts: 4,
	baseDelay:   time.Millisecond,
	maxDelay:    5 * time.Millisecond,
	budget:      10 * time.Second,
}

// retryTestServer stands up an httptest server whose handler is driven by the
// caller, and returns a Client wired the way NewClient wires the real one, plus
// a counter of requests the server actually saw. The handler is given the
// 1-based attempt number so a test can vary its answer per attempt.
func retryTestServer(t *testing.T, handler func(w http.ResponseWriter, attempt int64)) (*Client, *atomic.Int64) {
	t.Helper()

	var calls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handler(w, calls.Add(1))
	}))
	t.Cleanup(srv.Close)

	return &Client{
		Client: &retryClient{
			next:   graphql.NewClient(srv.URL, srv.Client()),
			policy: testRetryPolicy,
		},
		Endpoint: srv.URL,
	}, &calls
}

const workflowJSON = `{"data":{"workflow":{"id":"wf_123","name":"n","trigger":"{}",` +
	`"startStepId":null,"publishedAt":null,"order":1,"steps":[]}}}`

func TestRetryRecoversFromTransient500(t *testing.T) {
	t.Parallel()

	client, calls := retryTestServer(t, func(w http.ResponseWriter, attempt int64) {
		if attempt == 1 {
			http.Error(w, "internal server error", http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(workflowJSON))
	})

	resp, err := GetWorkflow(context.Background(), client, "wf_123")
	if err != nil {
		t.Fatalf("GetWorkflow after one 500: got error %v, want success", err)
	}

	if resp.Workflow.Id != "wf_123" {
		t.Errorf("workflow id = %q, want %q", resp.Workflow.Id, "wf_123")
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("server saw %d requests, want 2 (one 500, one success)", got)
	}
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	t.Parallel()

	client, calls := retryTestServer(t, func(w http.ResponseWriter, _ int64) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	_, err := GetWorkflow(context.Background(), client, "wf_123")
	if err == nil {
		t.Fatal("GetWorkflow against a permanently failing server: got nil error, want failure")
	}

	var httpErr *graphql.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("error = %v, want an *graphql.HTTPError with status 500", err)
	}

	if got := calls.Load(); got != int64(testRetryPolicy.maxAttempts) {
		t.Errorf("server saw %d requests, want %d", got, testRetryPolicy.maxAttempts)
	}
}

// Plain reports business errors in the payload with HTTP 200. Those are
// terminal — retrying one wastes the budget and, for a mutation, would be
// unsafe — so the client must hand the first response straight back.
func TestRetryDoesNotRetryPayloadErrorOn200(t *testing.T) {
	t.Parallel()

	const payloadError = `{"data":{"deleteWorkflow":{"error":{"message":"nope",` +
		`"type":"MUTATION_ERROR","code":"workflow_not_found","fields":[]}}}}`

	client, calls := retryTestServer(t, func(w http.ResponseWriter, _ int64) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payloadError))
	})

	resp, err := DeleteWorkflow(context.Background(), client, &DeleteWorkflowInput{WorkflowId: "wf_123"})
	if err != nil {
		t.Fatalf("DeleteWorkflow: got transport error %v, want the payload returned", err)
	}

	if resp.DeleteWorkflow.Error == nil {
		t.Fatal("DeleteWorkflow: payload error was not returned to the caller")
	}

	if got := resp.DeleteWorkflow.Error.Code; got != "workflow_not_found" {
		t.Errorf("error code = %q, want %q", got, "workflow_not_found")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 — a payload error must not be retried", got)
	}
}

// The allowlist is the whole safety argument: a lost 500 from a write may mean
// the write landed, so those operations get exactly one attempt.
func TestRetrySkipsNonIdempotentMutations(t *testing.T) {
	t.Parallel()

	client, calls := retryTestServer(t, func(w http.ResponseWriter, _ int64) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	_, err := CreateWorkflow(context.Background(), client, &CreateWorkflowInput{Name: "n"})
	if err == nil {
		t.Fatal("CreateWorkflow against a failing server: got nil error, want failure")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want 1 — CreateWorkflow must never be replayed", got)
	}
}

func TestRetryableOpsCoversOnlySafeOperations(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"CreateWorkflow", "UpdateWorkflow", "BulkUpsertWorkflowSteps"} {
		if retryableOps[op] {
			t.Errorf("%s is in retryableOps; replaying it risks a double write", op)
		}
	}

	for _, op := range []string{"GetWorkflow", "DeleteWorkflow"} {
		if !retryableOps[op] {
			t.Errorf("%s is not in retryableOps; transient 5xx will fail the operation", op)
		}
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		err  error
		want bool
	}{
		"http 500":          {&graphql.HTTPError{StatusCode: http.StatusInternalServerError}, true},
		"http 503":          {&graphql.HTTPError{StatusCode: http.StatusServiceUnavailable}, true},
		"http 403":          {&graphql.HTTPError{StatusCode: http.StatusForbidden}, false},
		"http 429":          {&graphql.HTTPError{StatusCode: http.StatusTooManyRequests}, false},
		"context canceled":  {context.Canceled, false},
		"deadline exceeded": {context.DeadlineExceeded, false},
		"decode failure":    {errString("invalid character '<'"), false},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := isRetryable(tc.err); got != tc.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A transport failure has no response at all, so it is retried. Pointing the
// client at a closed listener is the cheapest way to produce a real one.
func TestIsRetryableTransportError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	client := &retryClient{next: graphql.NewClient(url, http.DefaultClient), policy: testRetryPolicy}

	err := client.MakeRequest(context.Background(), &graphql.Request{OpName: "GetWorkflow"}, &graphql.Response{})
	if err == nil {
		t.Fatal("request to a closed listener: got nil error, want a transport failure")
	}

	if !isRetryable(err) {
		t.Errorf("isRetryable(%v) = false, want true for a connection failure", err)
	}
}

func TestRetryBackoffIsJitteredAndBounded(t *testing.T) {
	t.Parallel()

	policy := retryPolicy{maxAttempts: 4, baseDelay: 100 * time.Millisecond, maxDelay: 8 * time.Second}

	seen := map[time.Duration]bool{}

	for range 200 {
		for retry, want := range map[int]time.Duration{
			1: 100 * time.Millisecond,
			2: 200 * time.Millisecond,
			3: 400 * time.Millisecond,
		} {
			got := policy.backoff(retry)
			if got < 0 || got >= want {
				t.Fatalf("backoff(%d) = %v, want within [0, %v)", retry, got, want)
			}

			seen[got] = true
		}
	}

	if len(seen) < 3 {
		t.Errorf("backoff produced %d distinct values over 600 draws; it is not jittered", len(seen))
	}
}

// A very large retry number must not overflow the shift into a negative or
// wrapped duration; it clamps at maxDelay.
func TestRetryBackoffClampsAtMaxDelay(t *testing.T) {
	t.Parallel()

	policy := retryPolicy{baseDelay: 500 * time.Millisecond, maxDelay: 8 * time.Second}

	for _, retry := range []int{5, 32, 64, 100} {
		if got := policy.backoff(retry); got < 0 || got >= policy.maxDelay {
			t.Errorf("backoff(%d) = %v, want within [0, %v)", retry, got, policy.maxDelay)
		}
	}
}

// The retry budget must fit inside the client timeout, not extend past it.
func TestRetryBudgetFitsClientTimeout(t *testing.T) {
	t.Parallel()

	if defaultRetryPolicy.budget > clientTimeout {
		t.Errorf("retry budget %v exceeds the client timeout %v", defaultRetryPolicy.budget, clientTimeout)
	}

	var worst time.Duration
	for retry := 1; retry < defaultRetryPolicy.maxAttempts; retry++ {
		worst += min(defaultRetryPolicy.baseDelay<<(retry-1), defaultRetryPolicy.maxDelay)
	}

	if worst >= defaultRetryPolicy.budget {
		t.Errorf("worst-case backoff %v leaves no room inside the %v budget", worst, defaultRetryPolicy.budget)
	}
}

// An exhausted budget stops the sequence early rather than sleeping past it,
// and surfaces the underlying 5xx rather than a deadline error.
func TestRetryStopsWhenBudgetExhausted(t *testing.T) {
	t.Parallel()

	client, calls := retryTestServer(t, func(w http.ResponseWriter, _ int64) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	})

	// A backoff far larger than the budget: the next wait can never finish
	// inside it, so the sequence must be abandoned instead of slept through.
	client.Client.(*retryClient).policy = retryPolicy{
		maxAttempts: 10,
		baseDelay:   10 * time.Minute,
		maxDelay:    10 * time.Minute,
		budget:      10 * time.Millisecond,
	}

	start := time.Now()

	_, err := GetWorkflow(context.Background(), client, "wf_123")
	if err == nil {
		t.Fatal("GetWorkflow: got nil error, want failure")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v, want the sequence abandoned inside the 10ms budget", elapsed)
	}

	var httpErr *graphql.HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("error = %v, want the underlying HTTP error, not a deadline error", err)
	}

	if got := calls.Load(); got >= 10 {
		t.Errorf("server saw %d requests, want fewer than maxAttempts once the budget ran out", got)
	}
}

// errString is a plain error with no wrapped transport or HTTP cause, standing
// in for the response-decode failures isRetryable must not retry.
type errString string

func (e errString) Error() string { return string(e) }
