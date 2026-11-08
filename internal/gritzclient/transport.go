package gritzclient

import (
	"net/http"

	"golang.org/x/oauth2"
)

// AuthTransport injects a Bearer token from Source into requests, asking it
// on every request. When ClientID is non-empty, it is also sent as
// X-Client-ID so the server can tag the resulting notifications with the
// originator's id and the originator can filter them back out.
type AuthTransport struct {
	Transport http.RoundTripper
	Source    oauth2.TokenSource
	ClientID  string
}

func (t *AuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.Source.Token()
	if err != nil {
		// A RoundTripper must close the body, even on error.
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if t.ClientID != "" {
		req.Header.Set("X-Client-ID", t.ClientID)
	}
	return t.Transport.RoundTrip(req)
}

// StaticToken returns a TokenSource that always returns token.
func StaticToken(token string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
}
