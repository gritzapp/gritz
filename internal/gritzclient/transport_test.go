package gritzclient_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
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
				Token:     "abc",
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
