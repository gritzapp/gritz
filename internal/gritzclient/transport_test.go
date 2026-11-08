package gritzclient_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	"golang.org/x/oauth2"
	"gotest.tools/v3/assert"
)

func TestAuthTransport_SetsHeaders(t *testing.T) {
	t.Parallel()
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	cases := []struct {
		name          string
		clientID      string
		wantClientHdr string
	}{
		{name: "without client id", clientID: "", wantClientHdr: ""},
		{name: "with client id", clientID: "bridge-42", wantClientHdr: "bridge-42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: &gritzclient.AuthTransport{
				Transport: http.DefaultTransport,
				Source:    gritzclient.StaticToken("abc"),
				ClientID:  tc.clientID,
			}}
			req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
			assert.NilError(t, err)
			resp, err := client.Do(req)
			assert.NilError(t, err)
			resp.Body.Close()

			h := <-got
			assert.Equal(t, h.Get("Authorization"), "Bearer abc")
			assert.Equal(t, h.Get("X-Client-ID"), tc.wantClientHdr)
		})
	}
}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func TestAuthTransport_AsksSourceEachRequest(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get("Authorization")
	}))
	t.Cleanup(srv.Close)

	tokens := []string{"first", "second"}
	errFailed := errors.New("refresh failed")
	client := &http.Client{Transport: &gritzclient.AuthTransport{
		Transport: http.DefaultTransport,
		Source: tokenSourceFunc(func() (*oauth2.Token, error) {
			if len(tokens) == 0 {
				return nil, errFailed
			}
			tok := &oauth2.Token{AccessToken: tokens[0]}
			tokens = tokens[1:]
			return tok, nil
		}),
	}}

	for _, want := range []string{"Bearer first", "Bearer second"} {
		resp, err := client.Get(srv.URL)
		assert.NilError(t, err)
		resp.Body.Close()
		assert.Equal(t, <-got, want)
	}
	_, err := client.Get(srv.URL)
	assert.ErrorIs(t, err, errFailed)
}
