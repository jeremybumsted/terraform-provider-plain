package plain

import (
	"net/http"
	"time"

	"github.com/Khan/genqlient/graphql"
)

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

// NewClient builds a Plain API client that authenticates every request.
func NewClient(endpoint, apiKey, version string) *Client {
	httpClient := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &authTransport{
			apiKey:    apiKey,
			userAgent: "terraform-provider-plain/" + version,
			next:      http.DefaultTransport,
		},
	}

	return &Client{
		Client:   graphql.NewClient(endpoint, httpClient),
		Endpoint: endpoint,
	}
}
