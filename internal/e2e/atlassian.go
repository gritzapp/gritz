package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/atlassian"
)

// AtlassianWebhook is an org's Atlassian webhook, as Jira is configured with it.
type AtlassianWebhook struct {
	// Resp is the GenerateAtlassianWebhookSecret response the webhook was made
	// from.
	Resp *gritzv1.GenerateAtlassianWebhookSecretResponse

	org *Org
}

// GenerateAtlassianWebhookSecret generates the org's Atlassian webhook secret,
// as the settings page does.
func (o *Org) GenerateAtlassianWebhookSecret(req *gritzv1.GenerateAtlassianWebhookSecretRequest) *AtlassianWebhook {
	t := o.server.t
	t.Helper()
	resp, err := o.Client.GenerateAtlassianWebhookSecret(t.Context(), req)
	if err != nil {
		t.Fatalf("GenerateAtlassianWebhookSecret: %v", err)
	}
	return &AtlassianWebhook{Resp: resp, org: o}
}

// WebhookResponse is the server's reply to a webhook delivery.
type WebhookResponse struct {
	StatusCode int
	Body       string
}

// Send delivers payload to the webhook, as Jira does: marshaled to JSON,
// posted to its URL and signed with its secret.
func (w *AtlassianWebhook) Send(payload *atlassian.WebhookPayload) *WebhookResponse {
	t := w.org.server.t
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, w.Resp.WebhookUrl, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	req.Header.Set("X-Hub-Signature", atlassian.SignWebhook(data, w.Resp.Secret))
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
