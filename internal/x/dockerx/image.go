package dockerx

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/distribution/reference"
	"github.com/docker/cli/cli/config"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/registry"
	"github.com/docker/docker/client"
)

// PullProgress represents a progress update from a Docker image pull.
type PullProgress struct {
	Status   string `json:"status"`
	ID       string `json:"id"`
	Progress string `json:"progress"`
}

// ImageEnsureOptions configures the ImageEnsure function.
type ImageEnsureOptions struct {
	Ref          string
	PullOptions  image.PullOptions
	PullProgress func(PullProgress)
}

// ImageEnsure ensures the image is available locally, pulling it if necessary.
// It returns the image inspect result.
func ImageEnsure(ctx context.Context, docker *client.Client, opts ImageEnsureOptions) (image.InspectResponse, error) {
	info, err := docker.ImageInspect(ctx, opts.Ref)
	if err == nil {
		return info, nil
	}
	reader, err := docker.ImagePull(ctx, opts.Ref, opts.PullOptions)
	if err != nil {
		return image.InspectResponse{}, fmt.Errorf("failed to pull image: %w", err)
	}
	defer reader.Close()
	dec := jsontext.NewDecoder(reader)
	for {
		var msg PullProgress
		if err := json.UnmarshalDecode(dec, &msg); err != nil {
			break
		}
		if opts.PullProgress != nil {
			opts.PullProgress(msg)
		}
	}
	info, err = docker.ImageInspect(ctx, opts.Ref)
	if err != nil {
		return image.InspectResponse{}, fmt.Errorf("failed to inspect image after pull: %w", err)
	}
	return info, nil
}

// ResolveRegistryAuth returns the base64-encoded registry auth for the given
// image reference by reading ~/.docker/config.json. It returns an empty string
// if no credentials are configured for the registry.
func ResolveRegistryAuth(imageRef string) string {
	ref, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return ""
	}
	cfg, err := config.Load("")
	if err != nil {
		return ""
	}
	authConfig, err := cfg.GetAuthConfig(reference.Domain(ref))
	if err != nil {
		return ""
	}
	if authConfig.Username == "" && authConfig.Password == "" && authConfig.IdentityToken == "" {
		return ""
	}
	encoded, err := registry.EncodeAuthConfig(registry.AuthConfig{
		Username:      authConfig.Username,
		Password:      authConfig.Password,
		Auth:          authConfig.Auth,
		ServerAddress: authConfig.ServerAddress,
		IdentityToken: authConfig.IdentityToken,
		RegistryToken: authConfig.RegistryToken,
	})
	if err != nil {
		return ""
	}
	return encoded
}
