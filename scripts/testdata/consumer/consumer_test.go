package consumer_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/content/memory"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/load"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Compile the containerd client against its selected API as well as the SDK's
// old fetch aliases and the shared loader contract. No daemon is needed.
var (
	_                   = containerd.New
	_ load.Loader       = fetch.KitLoader(nil)
	_ fetch.KitLoader   = load.Loader(nil)
	_ load.LayerLoader  = fetch.LayerLoader(nil)
	_ fetch.LayerLoader = load.LayerLoader(nil)
	_ *load.Kit         = (*fetch.LoadedKit)(nil)
)

func TestAssembleAndFetchWithContainerd23(t *testing.T) {
	ctx := t.Context()
	var layer bytes.Buffer
	tw := tar.NewWriter(&layer)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "hello", Mode: 0o644, Size: 5}))
	_, err := tw.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	config := ocispec.Image{
		Platform: ocispec.Platform{OS: "linux", Architecture: "amd64"},
		RootFS:   ocispec.RootFS{Type: "layers", DiffIDs: []digest.Digest{digest.FromBytes(layer.Bytes())}},
		Config:   ocispec.ImageConfig{Env: []string{"HOME=/home/agent"}, WorkingDir: "/workspace"},
	}
	configRaw, err := json.Marshal(config)
	require.NoError(t, err)
	descriptor, err := json.Marshal(spec.Descriptor{
		SchemaVersion: spec.SchemaVersion, Kind: spec.KindWorkload, Provides: []string{"base@1.0.0"},
	})
	require.NoError(t, err)
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig, Digest: digest.FromBytes(configRaw), Size: int64(len(configRaw))},
		Layers: []ocispec.Descriptor{{MediaType: ocispec.MediaTypeImageLayer, Digest: digest.FromBytes(layer.Bytes()), Size: int64(layer.Len())}},
	}
	input := &fetch.LoadedKit{
		Digest: digest.FromString("base"), Manifest: manifest, Config: config, Descriptor: descriptor,
		LayerLoader: func(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(layer.Bytes())), nil
		},
	}
	var loader load.Loader = func(context.Context, string) (*load.Kit, error) { return input, nil }
	result, err := fetch.Assemble(ctx, []fetch.Request{{Reference: "example.com/base:1.0.0"}}, fetch.Options{
		Loader: loader, LayerValidator: fetch.DefaultLayerValidator,
		Overrides: fetch.Overrides{Env: map[string]string{"TEAM": "runtime"}},
	})
	require.NoError(t, err)
	require.Equal(t, "runtime", result.Environment["TEAM"])
	require.Equal(t, "/home/agent", result.Environment["HOME"])
	require.Equal(t, "/workspace", result.WorkingDir)

	image, err := assemble.Assemble(ctx, result.Resolved.Kits, func(context.Context, string) (assemble.Input, error) {
		return assemble.Input{Manifest: manifest, Config: config}, nil
	})
	require.NoError(t, err)
	require.Equal(t, image, result.Image)
	store := memory.New()
	published, err := image.WriteMetadata(ctx, store)
	require.NoError(t, err)
	exists, err := store.Exists(ctx, published)
	require.NoError(t, err)
	require.True(t, exists, "assembled metadata was not written")

	client, err := fetch.New(fetch.WithPlatform(config.Platform))
	require.NoError(t, err)
	var _ assemble.ImageLoader = client.LoadImage
	var _ load.Loader = client.LoadKit
}
