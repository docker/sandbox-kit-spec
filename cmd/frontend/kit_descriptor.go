package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/client/llb"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// resolvePublishedDescriptor reads the identity of the image already
// resolved by BuildKit. Always use the pin: a second resolution of a
// moving tag could select declarations for different content.
func resolvePublishedDescriptor(ctx context.Context, c gwclient.Client, pinned string, p ocispecs.Platform) (*spec.Descriptor, error) {
	named, err := reference.ParseNormalizedNamed(pinned)
	if err != nil {
		return nil, err
	}
	_, ok := named.(reference.Canonical)
	if !ok {
		return nil, fmt.Errorf("published descriptor lookup needs a digest-pinned image, got %q", pinned)
	}

	// ImageBlob uses the builder's registry resolver and session, so
	// manifest reads share the image pull's credentials, mirrors, CAs,
	// and network namespace. The transport only adapts those reads to
	// fetch.Client's bounded, platform-aware metadata walk.
	client, err := fetch.New(fetch.WithPlatform(p), fetch.WithTransport(builderManifestTransport{
		client: c, repository: reference.TrimNamed(named),
	}))
	if err != nil {
		return nil, err
	}
	kit, err := client.Fetch(ctx, pinned)
	if err != nil {
		return nil, fmt.Errorf("read published descriptor from %s: %w", pinned, err)
	}
	return kit.Descriptor, nil
}

// builderManifestTransport retrieves every manifest through BuildKit,
// including private registries and localhost. It never makes an HTTP
// request from the frontend or resolves a mutable tag a second time.
type builderManifestTransport struct {
	client     gwclient.Client
	repository reference.Named
}

func (t builderManifestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	prefix := "/v2/" + reference.Path(t.repository) + "/manifests/"
	if req.Method != http.MethodGet || !strings.HasPrefix(req.URL.Path, prefix) {
		return nil, fmt.Errorf("descriptor lookup supports only digest-addressed manifest GETs")
	}
	dgst, err := digest.Parse(strings.TrimPrefix(req.URL.Path, prefix))
	if err != nil {
		return nil, fmt.Errorf("manifest lookup needs a digest: %w", err)
	}
	pinned, err := reference.WithDigest(t.repository, dgst)
	if err != nil {
		return nil, err
	}
	const filename = "kit-manifest.json"
	state := llb.ImageBlob(pinned.String(), llb.Filename(filename),
		llb.WithCustomName("[kit] manifest "+pinned.String()))
	def, err := state.Marshal(req.Context())
	if err != nil {
		return nil, err
	}
	res, err := t.client.Solve(req.Context(), gwclient.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return nil, err
	}
	ref, err := res.SingleRef()
	if err != nil {
		return nil, err
	}
	// Bound the gateway read too: fetch.Client's metadata limit applies
	// after this transport has handed it a response body.
	const maxManifestBytes = 8 << 20
	body, err := ref.ReadFile(req.Context(), gwclient.ReadRequest{
		Filename: "/" + filename, Range: &gwclient.FileRange{Length: maxManifestBytes + 1},
	})
	if err != nil {
		return nil, err
	}
	if len(body) > maxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestBytes)
	}
	var metadata struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("decode downloaded manifest: %w", err)
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {metadata.MediaType}, "Docker-Content-Digest": {dgst.String()}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}, nil
}

// matchStagedDescriptor compares whole documents: YAML and compact JSON
// differ as bytes, while a comparison of selected fields could silently
// accept another kit's permissions. Identical copies in different roots
// remain ambiguous because stem-based source collision checks need the
// actual root, not just an equivalent set of declarations.
func matchStagedDescriptor(published *spec.Descriptor, staged map[string][]byte) (*spec.Descriptor, string, error) {
	want, err := json.Marshal(published)
	if err != nil {
		return nil, "", err
	}
	var matches []string
	for stem, raw := range staged {
		d, err := spec.Decode(raw)
		if err != nil {
			continue
		}
		got, err := json.Marshal(d)
		if err != nil {
			return nil, "", err
		}
		if bytes.Equal(got, want) {
			matches = append(matches, stem)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 0:
		return nil, "", fmt.Errorf("no staged descriptor matches the published annotation")
	case 1:
		return published, matches[0], nil
	default:
		return nil, "", fmt.Errorf("the published descriptor matches several staged roots (%s); its own source directory is ambiguous", strings.Join(matches, ", "))
	}
}
