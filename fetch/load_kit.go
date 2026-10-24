package fetch

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/sandbox-kit-spec/v3/load"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// KitLoader is load.Loader, retained for compatibility.
// Assemble always validates declarations. DefaultLayerValidator verifies both blob
// digests and diff IDs when selected as Options.LayerValidator.
type KitLoader = load.Loader

// LayerLoader is load.LayerLoader, retained for compatibility.
type LayerLoader = load.LayerLoader

// LoadedKit is load.Kit, retained for compatibility.
// Assemble does not mutate it. DefaultLayerValidator requires a non-nil
// LayerLoader and closes each returned stream.
type LoadedKit = load.Kit

// LoadKit implements KitLoader with this client's credentials and platform.
// Image metadata is loaded by the resolved digest to prevent tag movement
// between descriptor and image reads. Filesystem layers are opened lazily.
func (c *Client) LoadKit(ctx context.Context, ref string) (*LoadedKit, error) {
	kit, err := c.Fetch(ctx, ref)
	if err != nil {
		return nil, err
	}
	pinned, err := pinnedImage(ref, kit.Digest)
	if err != nil {
		return nil, err
	}
	image, err := c.LoadImage(ctx, pinned)
	if err != nil {
		return nil, err
	}
	repoName, _, err := splitRef(pinned)
	if err != nil {
		return nil, err
	}
	repo, err := c.repository(repoName)
	if err != nil {
		return nil, err
	}
	return &LoadedKit{
		Digest: digest.Digest(kit.Digest), Manifest: image.Manifest, Config: image.Config, Image: pinned, Descriptor: kit.Raw,
		LayerLoader: func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error) {
			reader, err := repo.Fetch(ctx, layer)
			if err != nil {
				return nil, fmt.Errorf("open layer %s: %w", layer.Digest, err)
			}
			return reader, nil
		},
	}, nil
}
