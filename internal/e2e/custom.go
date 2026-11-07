package e2e

import (
	"io"
	"net/http"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/server/customserver"
)

// CustomWebhook is an org's custom event webhook, as an external system is
// configured with it: the webhook URL and an API key.
type CustomWebhook struct {
	// Resp is the GetOrgSettings response the webhook URL was read from.
	Resp *gritzv1.GetOrgSettingsResponse

	org   *Org
	token string
}

// CustomWebhook returns the org's custom event webhook authenticated with key,
// reading its URL from GetOrgSettings as the settings page does.
func (o *Org) CustomWebhook(key *Key) *CustomWebhook {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GetOrgSettings(t.Context(), &gritzv1.GetOrgSettingsRequest{})
	if err != nil {
		t.Fatalf("GetOrgSettings: %v", err)
	}
	return &CustomWebhook{Resp: resp, org: o, token: key.Resp.RawToken}
}

// Send submits env to the webhook, as an external system does: marshaled to
// JSON and posted to its URL with the key as a bearer token. The harness
// server has no BaseURL, so the webhook URL is only a path on it.
func (w *CustomWebhook) Send(env customserver.Envelope) *WebhookResponse {
	t := w.org.server.t
	t.Helper()
	req, err := customserver.NewWebhookRequest(t.Context(), w.org.server.URL+w.Resp.CustomWebhookUrl, w.token, env)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	return &WebhookResponse{StatusCode: resp.StatusCode, Body: string(body)}
}
