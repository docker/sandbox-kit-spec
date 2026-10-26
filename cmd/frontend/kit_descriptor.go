package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
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

	// The gateway exposes image config, not the descriptor annotation.
	// Read the pinned manifest through the same bounded registry reader
	// as other Kit consumers. This process does not receive BuildKit's
	// registry credentials or mirror configuration.
	// The attestation-chain API is not a substitute: it omits platform
	// manifest bodies, and current BuildKit releases dereference a nil
	// chain when asked for metadata of a bare image manifest.
	options := []fetch.Option{fetch.WithPlatform(p)}
	if c != nil {
		options = append(options, fetch.WithTransport(manifestTransport{client: c}))
	}
	client, err := fetch.New(options...)
	if err != nil {
		return nil, err
	}
	kit, err := client.Fetch(ctx, pinned)
	if err != nil {
		return nil, fmt.Errorf("read published descriptor from %s: %w; descriptor lookup is anonymous and cannot use the builder's registry credentials or mirrors", pinned, err)
	}
	return kit.Descriptor, nil
}

// manifestTransport keeps loopback registry references in the builder's
// network namespace. A frontend has its own loopback, so an ordinary
// HTTP client cannot reach a registry BuildKit resolves as localhost.
// Only anonymous, digest-addressed HTTP GETs use this source: it is not
// a general HTTP transport and cannot relay authentication challenges.
type manifestTransport struct{ client gwclient.Client }

func (t manifestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	ip := net.ParseIP(host)
	if req.URL.Scheme != "http" || (host != "localhost" && (ip == nil || !ip.IsLoopback())) {
		return http.DefaultTransport.RoundTrip(req)
	}
	if req.Method != http.MethodGet || req.Header.Get("Authorization") != "" || !strings.Contains(req.URL.Path, "/manifests/") {
		return nil, fmt.Errorf("loopback descriptor lookup supports only anonymous manifest GETs")
	}
	dgst, err := digest.Parse(path.Base(req.URL.Path))
	if err != nil {
		return nil, fmt.Errorf("loopback manifest lookup needs a digest: %w", err)
	}
	const filename = "kit-manifest.json"
	state := llb.HTTP(req.URL.String(), llb.Filename(filename), llb.Checksum(dgst),
		llb.Header(llb.HTTPHeader{Accept: req.Header.Get("Accept")}))
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
	body, ok, err := readImageFile(req.Context(), ref, "/"+filename)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("BuildKit HTTP source returned no manifest")
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
