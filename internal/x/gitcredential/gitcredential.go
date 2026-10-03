package gitcredential

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/icholy/gritz/internal/gritzclient"
	gritzv1 "github.com/icholy/gritz/internal/proto/gritz/v1"
)

// Run executes the git credential helper logic for the given action.
func Run(ctx context.Context, action string, r io.Reader, w io.Writer, client gritzclient.Client) error {
	switch action {
	case "get":
		return get(ctx, r, w, client)
	case "store", "erase":
		_, _ = io.Copy(io.Discard, r)
		return nil
	default:
		// Unknown actions exit 0 per git spec.
		return nil
	}
}

// ParseInput parses the key=value block from git's credential helper stdin.
func ParseInput(r io.Reader) map[string]string {
	result := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			result[key] = value
		}
	}
	return result
}

// FormatOutput writes credential output in the git key=value format.
func FormatOutput(w io.Writer, token string) error {
	_, err := fmt.Fprintf(w, "username=x-access-token\npassword=%s\n\n", token)
	return err
}

func get(ctx context.Context, r io.Reader, w io.Writer, client gritzclient.Client) error {
	fields := ParseInput(r)

	// Only handle github.com over https.
	if fields["host"] != "github.com" {
		return nil
	}
	if protocol, ok := fields["protocol"]; ok && protocol != "https" {
		return nil
	}

	resp, err := client.CreateGitHubToken(ctx, &gritzv1.CreateGitHubTokenRequest{})
	if err != nil {
		return fmt.Errorf("failed to get GitHub token: %w", err)
	}

	return FormatOutput(w, resp.Token)
}
