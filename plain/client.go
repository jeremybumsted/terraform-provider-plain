package plain

import (
	"net/http"
	"time"

	"github.com/Khan/genqlient/graphql"
)

// clientTimeout bounds a single call to Plain, retries included: it is both the
// per-attempt HTTP timeout and the whole retry budget (see retry.go).
const clientTimeout = 60 * time.Second

// Client is a configured GraphQL client for the Plain API.
type Client struct {
	graphql.Client
	Endpoint string
}

type authTransport struct {
	apiKey    string
	userAgent string
	next      http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone before mutating: RoundTrippers must not modify the caller's request.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("User-Agent", t.userAgent)
	return t.next.RoundTrip(req)
}

// NewClient builds a Plain API client that authenticates every request and
// retries transient failures on the operations where a replay is safe.
func NewClient(endpoint, apiKey, version string) *Client {
	httpClient := &http.Client{
		Timeout: clientTimeout,
		Transport: &authTransport{
			apiKey:    apiKey,
			userAgent: "terraform-provider-plain/" + version,
			next:      http.DefaultTransport,
		},
	}

	return &Client{
		Client: &retryClient{
			next:   graphql.NewClient(endpoint, httpClient),
			policy: defaultRetryPolicy,
		},
		Endpoint: endpoint,
	}
}
