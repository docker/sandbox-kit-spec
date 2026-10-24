// Package load defines the Kit loader contract without a registry client.
// Runtimes and content stores can share these types without importing fetch.
package load

import (
	"context"
	"io"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Loader supplies verified metadata for the requested reference. It owns
// authentication, platform selection, and its backing store. The returned
// digest pins the requested manifest or index, not a subsequently moved tag.
// Consumers validate declarations and, when needed, layer integrity separately.
type Loader func(context.Context, string) (*Kit, error)

// LayerLoader opens a fresh stream of a manifest layer in its original
// compression. Reads must honor ctx; the caller closes the returned stream.
type LayerLoader func(ctx context.Context, layer ocispec.Descriptor) (io.ReadCloser, error)

// Kit keeps descriptor identity and image metadata together. Its data must
// remain stable while in use by a consumer; consumers treat it as read-only.
type Kit struct {
	Digest   digest.Digest
	Manifest ocispec.Manifest
	Config   ocispec.Image

	// Image is the runtime-resolvable reference for this Kit's content, such
	// as the store-local tag a source Kit was built under. The loader must
	// keep it consistent with the returned digest and metadata. When empty,
	// fetch.Assemble derives a digest-pinned image from the request reference.
	Image string

	// Descriptor is the published annotation, including an index annotation when
	// present. If empty, fetch.Assemble reads the selected manifest's annotation.
	Descriptor []byte
	// LayerLoader opens a fresh stream of the blob as described by the manifest,
	// with its original compression. The consumer closes each returned stream.
	// Nil is allowed for metadata-only use; layer validation requires it.
	LayerLoader LayerLoader
}
