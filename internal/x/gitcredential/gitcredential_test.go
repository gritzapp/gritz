package gitcredential

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gritzapp/gritz/internal/gritzclient"
	gritzv1 "github.com/gritzapp/gritz/internal/proto/gritz/v1"
)

func TestParseInput(t *testing.T) {
	input := "protocol=https\nhost=github.com\npath=icholy/gritz.git\n\n"
	fields := ParseInput(strings.NewReader(input))

	if fields["protocol"] != "https" {
		t.Errorf("expected protocol=https, got %q", fields["protocol"])
	}
	if fields["host"] != "github.com" {
		t.Errorf("expected host=github.com, got %q", fields["host"])
	}
	if fields["path"] != "icholy/gritz.git" {
		t.Errorf("expected path=icholy/gritz.git, got %q", fields["path"])
	}
}

func TestFormatOutput(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatOutput(&buf, "ghs_abc123"); err != nil {
		t.Fatal(err)
	}
	want := "username=x-access-token\npassword=ghs_abc123\n\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunGet(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return &gritzv1.CreateGitHubTokenResponse{Token: "ghs_test_token"}, nil
		},
	}

	input := "protocol=https\nhost=github.com\n\n"
	var output bytes.Buffer
	err := Run(context.Background(), "get", strings.NewReader(input), &output, client)
	if err != nil {
		t.Fatal(err)
	}

	want := "username=x-access-token\npassword=ghs_test_token\n\n"
	if got := output.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRunGetNonGitHub(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			t.Fatal("CreateGitHubToken should not be called for non-GitHub hosts")
			return nil, nil
		},
	}

	input := "protocol=https\nhost=gitlab.com\n\n"
	var output bytes.Buffer
	err := Run(context.Background(), "get", strings.NewReader(input), &output, client)
	if err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Errorf("expected no output for non-GitHub host, got %q", output.String())
	}
}

func TestRunGetNonHTTPS(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			t.Fatal("CreateGitHubToken should not be called for non-HTTPS protocol")
			return nil, nil
		},
	}

	input := "protocol=http\nhost=github.com\n\n"
	var output bytes.Buffer
	err := Run(context.Background(), "get", strings.NewReader(input), &output, client)
	if err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Errorf("expected no output for non-HTTPS protocol, got %q", output.String())
	}
}

func TestRunStoreNoop(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), "store", strings.NewReader("protocol=https\nhost=github.com\n\n"), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Errorf("expected no output for store, got %q", output.String())
	}
}

func TestRunEraseNoop(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), "erase", strings.NewReader("protocol=https\nhost=github.com\n\n"), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Errorf("expected no output for erase, got %q", output.String())
	}
}

func TestRunUnknownAction(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), "unknown", strings.NewReader(""), &output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Errorf("expected no output for unknown action, got %q", output.String())
	}
}

func TestRunRPCError(t *testing.T) {
	client := &gritzclient.ClientMock{
		CreateGitHubTokenFunc: func(_ context.Context, _ *gritzv1.CreateGitHubTokenRequest) (*gritzv1.CreateGitHubTokenResponse, error) {
			return nil, fmt.Errorf("no GitHub App installation linked to this org")
		},
	}

	input := "protocol=https\nhost=github.com\n\n"
	var output bytes.Buffer
	err := Run(context.Background(), "get", strings.NewReader(input), &output, client)
	if err == nil {
		t.Fatal("expected error from RPC failure")
	}
	if !strings.Contains(err.Error(), "failed to get GitHub token") {
		t.Errorf("unexpected error message: %v", err)
	}
}
