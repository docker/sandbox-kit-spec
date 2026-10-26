package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"

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
	staged map[string][]byte
}

func (c *descriptorGateway) Solve(ctx context.Context, req gwclient.SolveRequest) (*gwclient.Result, error) {
	res := gwclient.NewResult()
	for _, raw := range req.Definition.Def {
		var op pb.Op
		if err := op.UnmarshalVT(raw); err != nil {
			return nil, err
		}
		if source := op.GetSource(); source != nil && strings.HasPrefix(source.Identifier, "http://") {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.Identifier, nil)
			if err != nil {
				return nil, err
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return nil, err
			}
			defer func() { _ = response.Body.Close() }()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				return nil, err
			}
			res.SetRef(descriptorReference{manifest: body})
			return res, nil
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
		return r.manifest, nil
	}
	for stem, raw := range r.staged {
		if req.Filename == path.Join(stagedKitRoot, stem, stagedDescriptorName) {
			return raw, nil
		}
	}
	return nil, os.ErrNotExist
}

// descriptorRegistry serves metadata only, and accepts the digest pin
// rather than the mutable tag used to name the image.
func descriptorRegistry(t *testing.T, annotation string) string {
	t.Helper()
	raw, err := json.Marshal(ocispecs.Manifest{
		Versioned:   ocispec.Versioned{SchemaVersion: 2},
		MediaType:   ocispecs.MediaTypeImageManifest,
		Config:      ocispecs.Descriptor{MediaType: ocispecs.MediaTypeImageConfig},
		Annotations: map[string]string{spec.AnnotationDescriptor: annotation},
	})
	require.NoError(t, err)
	dgst := digest.FromBytes(raw)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/kit/manifests/"+dgst.String() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ocispecs.MediaTypeImageManifest)
		w.Header().Set("Docker-Content-Digest", dgst.String())
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://") + "/kit:mutable@" + dgst.String()
}

func TestReadKitDescriptorUsesPublishedIdentity(t *testing.T) {
	// The base looks like a merged set, so a kits:-based heuristic would
	// pick it. The outer kit's name also bears no relation to its stem.
	raw := `{"schemaVersion":"3","kind":"workload","version":"2.0.0","provides":["agent@2.0.0"]}`
	pinned := descriptorRegistry(t, raw)
	c := &descriptorGateway{}
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
		_, err := resolvePublishedDescriptor(t.Context(), nil, descriptorRegistry(t, annotation), p)
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
		blobs := map[string][]byte{rootDigest.String(): root, digest.FromBytes(arm64).String(): arm64}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := blobs[strings.TrimPrefix(r.URL.Path, "/v2/kit/manifests/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", ocispecs.MediaTypeImageManifest)
			if string(raw) == string(index) {
				w.Header().Set("Content-Type", ocispecs.MediaTypeImageIndex)
			}
			w.Header().Set("Docker-Content-Digest", digest.FromBytes(raw).String())
			_, _ = w.Write(raw)
		}))
		pinned := strings.TrimPrefix(server.URL, "http://") + "/kit:mutable@" + rootDigest.String()
		d, err := resolvePublishedDescriptor(t.Context(), nil, pinned, ocispecs.Platform{OS: "linux", Architecture: "arm64"})
		require.NoError(t, err)
		require.Equal(t, "2.0.0", d.Version)
		server.Close()
	}
}

func TestPublishedDescriptorExplainsPrivateRegistryAccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	pinned := strings.TrimPrefix(server.URL, "http://") + "/kit@" + digest.FromString("private").String()
	_, err := resolvePublishedDescriptor(t.Context(), nil, pinned, ocispecs.Platform{OS: "linux", Architecture: "amd64"})
	require.ErrorContains(t, err, "descriptor lookup is anonymous")
}

func TestLoopbackManifestLookupRequiresPinnedAnonymousGET(t *testing.T) {
	for _, tc := range []struct {
		method string
		path   string
		auth   string
	}{
		{method: http.MethodHead, path: "/v2/kit/manifests/" + digest.FromString("manifest").String()},
		{method: http.MethodGet, path: "/v2/kit/manifests/mutable"},
		{method: http.MethodGet, path: "/v2/kit/blobs/" + digest.FromString("manifest").String()},
		{method: http.MethodGet, path: "/v2/kit/manifests/" + digest.FromString("manifest").String(), auth: "Bearer test"},
	} {
		req, err := http.NewRequest(tc.method, "http://localhost:5000"+tc.path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", tc.auth)
		_, err = (manifestTransport{}).RoundTrip(req)
		require.Error(t, err)
	}
}
