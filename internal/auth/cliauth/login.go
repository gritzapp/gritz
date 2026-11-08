package cliauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

// LoginOptions configures Login.
type LoginOptions struct {
	// Server is the gritz server URL.
	Server string
	// OrgID, when non-zero, preselects the org on the approval page.
	OrgID int64
	// OpenBrowser opens the authorize URL; gritz login sets it to
	// browser.OpenURL. Login ignores its error, having already printed the URL.
	OpenBrowser func(url string) error
	// Prompt receives the authorize URL, always printed.
	Prompt io.Writer
	// Timeout is how long Login waits for the browser to come back.
	Timeout time.Duration
}

// Login runs the authorization code flow with PKCE over a loopback redirect
// and returns the exchanged token.
func Login(ctx context.Context, opts LoginOptions) (*oauth2.Token, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, opts.Timeout, fmt.Errorf("login timed out after %s", opts.Timeout))
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	config := Config(opts.Server, "http://"+ln.Addr().String()+"/callback")
	state := oauth2.GenerateVerifier()
	verifier := oauth2.GenerateVerifier()
	authOpts := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if opts.OrgID != 0 {
		authOpts = append(authOpts, oauth2.SetAuthURLParam("org", strconv.FormatInt(opts.OrgID, 10)))
	}
	authURL := config.AuthCodeURL(state, authOpts...)

	type result struct {
		token *oauth2.Token
		err   error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		var res result
		switch q := r.URL.Query(); {
		case q.Get("state") != state:
			res.err = errors.New("login failed: state mismatch")
		case q.Get("error") == "access_denied":
			res.err = errors.New("login denied")
		default:
			res.token, res.err = config.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(verifier))
		}
		if res.err != nil {
			http.Error(w, res.err.Error(), http.StatusBadRequest)
		} else {
			_, _ = io.WriteString(w, "Logged in, you can close this tab.\n")
		}
		// Only the first callback counts.
		select {
		case results <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.WithoutCancel(ctx)) }()

	fmt.Fprintf(opts.Prompt, "Opening %s in your browser.\n", authURL)
	_ = opts.OpenBrowser(authURL)

	select {
	case res := <-results:
		return res.token, res.err
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
