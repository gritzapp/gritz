//go:generate go tool moq -out client_moq.go . Client

package gritzclient

import (
	"cmp"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/oauth2"

	"github.com/gritzapp/gritz/internal/proto/gritz/v1/gritzv1connect"
)

// DefaultTimeout is the default timeout for RPC calls.
const DefaultTimeout = 30 * time.Second

// DefaultURL is the default gritz server URL.
const DefaultURL = "https://gritz.dev"

type Client = gritzv1connect.GritzServiceClient

// Options configures the gritz client.
type Options struct {
	// BaseURL is the server URL.
	BaseURL string
	// TokenSource supplies the bearer token per request.
	// If nil, no authentication is performed.
	TokenSource oauth2.TokenSource
	// Timeout is the timeout for RPC calls.
	// Defaults to DefaultTimeout if zero.
	Timeout time.Duration
	// ClientID, when non-empty, is sent on every request as the X-Client-ID
	// header so the server stamps the resulting notifications with this id.
	// Pair with NotificationClientOptions.ClientID to suppress
	// self-notifications.
	ClientID string
	// Retry configures the retry-with-backoff behaviour for unary calls.
	// Zero-valued fields fall back to their defaults; set MaxRetries to a
	// negative value to disable retries entirely.
	Retry RetryInterceptor
}

// New returns a Connect client.
func New(opts Options) Client {
	transport := http.DefaultTransport
	if opts.TokenSource != nil {
		transport = &AuthTransport{
			Transport: transport,
			Source:    opts.TokenSource,
			ClientID:  opts.ClientID,
		}
	}
	httpClient := &http.Client{Transport: transport, Timeout: cmp.Or(opts.Timeout, DefaultTimeout)}
	return gritzv1connect.NewGritzServiceClient(httpClient, opts.BaseURL,
		connect.WithInterceptors(opts.Retry),
	)
}
