package e2e

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
	"github.com/gritzapp/gritz/internal/x/atlassian"
)

// Atlassian plays Jira: it delivers webhooks to the server.
type Atlassian struct {
	t *testing.T
}

// WebhookResponse is the server's reply to a webhook delivery.
type WebhookResponse struct {
	StatusCode int
	Body       string
}

// SendWebhook delivers payload to an org's webhook, as Jira does: posted to
// its URL and signed with its secret.
func (a *Atlassian) SendWebhook(webhook *gritzv1.GenerateAtlassianWebhookSecretResponse, payload []byte) *WebhookResponse {
	t := a.t
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, webhook.WebhookUrl, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("SendWebhook: %v", err)
	}
	req.Header.Set("X-Hub-Signature", atlassian.SignWebhook(payload, webhook.Secret))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SendWebhook: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("SendWebhook: %v", err)
	}
	return &WebhookResponse{StatusCode: resp.StatusCode, Body: string(body)}
}
