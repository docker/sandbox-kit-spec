package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	fstypes "github.com/tonistiigi/fsutil/types"

	"github.com/docker/sandbox-kit-spec/v3/spec"
)

type descriptorGateway struct {
	fakeGatewayClient
	staged       map[string][]byte
	manifests    map[digest.Digest][]byte
	manifestRefs []string
	manifestErr  error
}

func (c *descriptorGateway) Solve(_ context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	res := gwclient.NewResult()
	for _, raw := range req.Definition.Def {
		var op pb.Op
		if err := op.UnmarshalVT(raw); err != nil {
			return nil, err
		}
		if source := op.GetSource(); source != nil {
			if ref, ok := strings.CutPrefix(source.Identifier, "docker-image+blob://"); ok {
				c.manifestRefs = append(c.manifestRefs, ref)
				if c.manifestErr != nil {
					return nil, c.manifestErr
				}
				named, err := reference.ParseNormalizedNamed(ref)
				if err != nil {
					return nil, err
				}
				body, ok := c.manifests[named.(reference.Canonical).Digest()]
				if !ok {
					return nil, fmt.Errorf("manifest not found: %s", ref)
				}
				res.SetRef(descriptorReference{manifest: body})
				return res, nil
			}
		}
	}
	res.SetRef(descriptorReference{staged: c.staged})
	return res, nil
}

type descriptorReference struct {
	fakeReference
	staged   map[string][]byte
	manifest []byte
}

func (r descriptorReference) ReadDir(_ context.Context, req gwclient.ReadDirRequest) ([]*fstypes.Stat, error) {
	if req.Path != stagedKitRoot {
		return nil, os.ErrNotExist
	}
	var entries []*fstypes.Stat
	for stem := range r.staged {
		entries = append(entries, &fstypes.Stat{Path: stem, Mode: uint32(os.ModeDir | 0o755)})
	}
	return entries, nil
}

func (r descriptorReference) ReadFile(_ context.Context, req gwclient.ReadRequest) ([]byte, error) {
	if req.Filename == "/kit-manifest.json" && r.manifest != nil {
		body := r.manifest
		if req.Range != nil && len(body) > req.Range.Length {
			body = body[:req.Range.Length]
		}
		return body, nil
	}
	for stem, raw := range r.staged {
		if req.Filename == path.Join(stagedKitRoot, stem, stagedDescriptorName) {
			return raw, nil
		}
	}
	return nil, os.ErrNotExist
}

// Only the fake builder has metadata: these references cannot resolve
// through an HTTP client in the frontend, and mutable tags are not keys.
func publishedDescriptor(t *testing.T, annotation string) (string, *descriptorGateway) {
	t.Helper()
	raw, err := json.Marshal(ocispecs.Manifest{
		Versioned:   ocispec.Versioned{SchemaVersion: 2},
		MediaType:   ocispecs.MediaTypeImageManifest,
		Config:      ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageConfig},
		Annotations: map[string]string{spec.AnnotationDescriptor: annotation},
	})
	require.NoError(t, err)
	dgst := digest.FromBytes(raw)
	return "private.example.com/kit:mutable@" + dgst.String(), &descriptorGateway{
		manifests: map[digest.Digest][]byte{dgst: raw},
	}
}

func TestReadKitDescriptorUsesPublishedIdentity(t *testing.T) {
	// The base looks like a merged set, so a kits:-based heuristic would
	// pick it. The outer kit's name also bears no relation to its stem.
	raw := `{"schemaVersion":"3","kind":"workload","version":"2.0.0","provides":["agent@2.0.0"]}`
	pinned, c := publishedDescriptor(t, raw)
	c.staged = map[string][]byte{
		"base-set":           []byte(`{"schemaVersion":"3","kind":"workload","version":"1.0.0","kits":[{"ref":"example.com/base","digest":"sha256:` + strings.Repeat("a", 64) + `"}]}`),
		"unrelated-filename": []byte("schemaVersion: \"3\"\nkind: workload\nversion: 2.0.0\nprovides: [agent@2.0.0]\n"),
		"broken":             []byte("not: [a descriptor"),
	}
	p := ocispecs.Platform{OS: "linux", Architecture: "arm64"}
	d, stem, err := readKitDescriptor(t.Context(), c, llb.Scratch(), p, pinned)
	require.NoError(t, err)
	require.Equal(t, "unrelated-filename", stem)
	require.Equal(t, []string{"agent@2.0.0"}, d.Provides)
	require.Equal(t, []string{strings.Replace(pinned, ":mutable@", "@", 1)}, c.manifestRefs,
		"the builder reads the same digest, with the mutable tag removed")
}

func TestReadSingleStagedDescriptorNeedsNoRegistryAccess(t *testing.T) {
	c := &descriptorGateway{staged: map[string][]byte{"only": []byte(`{"schemaVersion":"3","kind":"mixin"}`)}}
	_, stem, err := readKitDescriptor(t.Context(), c, llb.Scratch(), ocispecs.Platform{OS: "linux", Architecture: "amd64"}, "local-only")
	require.NoError(t, err)
	require.Equal(t, "only", stem)
}

func TestMatchStagedDescriptorRejectsMissingOrAmbiguousIdentity(t *testing.T) {
	d, err := spec.Decode([]byte(`{"schemaVersion":"3","kind":"mixin","description":"published"}`))
	require.NoError(t, err)
	for name, staged := range map[string]map[string][]byte{
		"missing":       {"other": []byte(`{"schemaVersion":"3","kind":"mixin","description":"different"}`)},
		"unknown field": {"other": []byte(`{"schemaVersion":"3","kind":"mixin","description":"published","unknown":true}`)},
		"ambiguous": {
			"a": []byte(`{"schemaVersion":"3","kind":"mixin","description":"published"}`),
			"b": []byte(`{"schemaVersion":"3","kind":"mixin","description":"published"}`),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := matchStagedDescriptor(d, staged)
			if name == "ambiguous" {
				require.ErrorContains(t, err, "several staged roots (a, b)")
			} else {
				require.ErrorContains(t, err, "no staged descriptor matches")
			}
		})
	}
}

func TestPublishedDescriptorRejectsUnpinnedOrInvalidMetadata(t *testing.T) {
	p := ocispecs.Platform{OS: "linux", Architecture: "amd64"}
	_, err := resolvePublishedDescriptor(t.Context(), nil, "example.com/kit:mutable", p)
	require.ErrorContains(t, err, "digest-pinned")
	for _, annotation := range []string{
		`{"schemaVersion":"3","kind":"set"}`,
		`{"schemaVersion":"3","kind":"mixin","unknown":true}`,
		"",
	} {
		pinned, c := publishedDescriptor(t, annotation)
		_, err := resolvePublishedDescriptor(t.Context(), c, pinned, p)
		require.Error(t, err)
	}
}

func TestPublishedDescriptorReadsPinnedPlatformManifest(t *testing.T) {
	manifest := func(version string) []byte {
		raw, err := json.Marshal(ocispecs.Manifest{
			Versioned: ocispec.Versioned{SchemaVersion: 2},
			MediaType: ocispecs.MediaTypeImageManifest,
			Config:    ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageConfig},
			Annotations: map[string]string{
				spec.AnnotationDescriptor: `{"schemaVersion":"3","kind":"mixin","version":"` + version + `"}`,
			},
		})
		require.NoError(t, err)
		return raw
	}
	amd64, arm64 := manifest("1.0.0"), manifest("2.0.0")
	index, err := json.Marshal(ocispecs.Index{
		Versioned: ocispec.Versioned{SchemaVersion: 2},
		MediaType: ocispecs.MediaTypeImageIndex,
		Manifests: []ocispecs.Descriptor{
			{MediaType: ocispecs.MediaTypeImageManifest, Digest: digest.FromBytes(amd64), Size: int64(len(amd64)), Platform: &ocispecs.Platform{OS: "linux", Architecture: "amd64"}},
			{MediaType: ocispecs.MediaTypeImageManifest, Digest: digest.FromBytes(arm64), Size: int64(len(arm64)), Platform: &ocispecs.Platform{OS: "linux", Architecture: "arm64"}},
		},
	})
	require.NoError(t, err)
	for _, root := range [][]byte{arm64, index} {
		rootDigest := digest.FromBytes(root)
		c := &descriptorGateway{manifests: map[digest.Digest][]byte{
			rootDigest: root, digest.FromBytes(arm64): arm64,
		}}
		pinned := "private.example.com/kit:mutable@" + rootDigest.String()
		d, err := resolvePublishedDescriptor(t.Context(), c, pinned, ocispecs.Platform{OS: "linux", Architecture: "arm64"})
		require.NoError(t, err)
		require.Equal(t, "2.0.0", d.Version)
	}
}

func TestPublishedDescriptorPropagatesBuilderErrors(t *testing.T) {
	pinned, c := publishedDescriptor(t, `{"schemaVersion":"3","kind":"mixin"}`)
	c.manifestErr = fmt.Errorf("builder registry resolver: unauthorized")
	_, err := resolvePublishedDescriptor(t.Context(), c, pinned, ocispecs.Platform{OS: "linux", Architecture: "amd64"})
	require.ErrorIs(t, err, c.manifestErr)
	require.Len(t, c.manifestRefs, 1, "a failed builder lookup has no anonymous HTTP fallback")
}

func TestPublishedDescriptorVerifiesBuilderManifest(t *testing.T) {
	pinned, c := publishedDescriptor(t, `{"schemaVersion":"3","kind":"mixin"}`)
	for dgst, raw := range c.manifests {
		c.manifests[dgst] = append(raw, ' ')
	}
	_, err := resolvePublishedDescriptor(t.Context(), c, pinned, ocispecs.Platform{OS: "linux", Architecture: "amd64"})
	require.Error(t, err, "the manifest must still match the resolved pin")
}

func TestBuilderManifestReadIsBounded(t *testing.T) {
	body := []byte(strings.Repeat(" ", (8<<20)+1))
	dgst := digest.FromBytes(body)
	c := &descriptorGateway{manifests: map[digest.Digest][]byte{dgst: body}}
	_, err := resolvePublishedDescriptor(t.Context(), c, "private.example.com/kit@"+dgst.String(), ocispecs.Platform{OS: "linux", Architecture: "amd64"})
	require.ErrorContains(t, err, "manifest exceeds")
}

func TestBuilderManifestLookupRequiresPinnedGET(t *testing.T) {
	repo, err := reference.ParseNormalizedNamed("private.example.com/kit")
	require.NoError(t, err)
	for _, tc := range []struct {
		method string
		path   string
	}{
		{method: http.MethodHead, path: "/v2/kit/manifests/" + digest.FromString("manifest").String()},
		{method: http.MethodGet, path: "/v2/kit/manifests/mutable"},
		{method: http.MethodGet, path: "/v2/kit/blobs/" + digest.FromString("manifest").String()},
		{method: http.MethodGet, path: "/v2/other/manifests/" + digest.FromString("manifest").String()},
	} {
		req, err := http.NewRequest(tc.method, "https://private.example.com"+tc.path, nil)
		require.NoError(t, err)
		_, err = (builderManifestTransport{repository: repo}).RoundTrip(req)
		require.Error(t, err)
	}
}
