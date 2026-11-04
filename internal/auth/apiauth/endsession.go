package apiauth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
	"github.com/zitadel/zitadel-go/v3/pkg/zitadel"
)

// refreshingHandler is the code flow handler with refresh enabled.
// authentication.Authenticator finds Refresh by type assertion, so a wrapper
// must keep it in its method set.
type refreshingHandler interface {
	authentication.Handler[*openid.DefaultContext]
	Refresh(ctx context.Context, authCtx *openid.DefaultContext) (*openid.DefaultContext, error)
}

// endSessionHandler replaces the code flow's RP-initiated logout, which builds
// its redirect from the discovered end_session_endpoint, with a redirect to a
// fixed URL.
type endSessionHandler struct {
	refreshingHandler
	url string
}

// Logout redirects to the configured URL. The Authenticator has already
// deleted the session cookie.
func (h *endSessionHandler) Logout(w http.ResponseWriter, r *http.Request, _ *openid.DefaultContext, _, _ string) {
	http.Redirect(w, r, h.url, http.StatusFound)
}

// withEndSessionEndpoint wraps init so that logout redirects to url verbatim.
func withEndSessionEndpoint(init authentication.HandlerInitializer[*openid.DefaultContext], url string) authentication.HandlerInitializer[*openid.DefaultContext] {
	return func(ctx context.Context, z *zitadel.Zitadel) (authentication.Handler[*openid.DefaultContext], error) {
		h, err := init(ctx, z)
		if err != nil {
			return nil, err
		}
		rh, ok := h.(refreshingHandler)
		if !ok {
			panic(fmt.Sprintf("apiauth: code flow handler %T has no Refresh method", h))
		}
		return &endSessionHandler{refreshingHandler: rh, url: url}, nil
	}
}
