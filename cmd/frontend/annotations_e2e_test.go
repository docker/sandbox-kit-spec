package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/spec"
)

// Inspect the registry output, not the frontend's pre-export self-check:
// default provenance can turn one platform into an image index after the
// frontend returns. Every image needs its metadata even when the frontend
// cannot predict that index; consumers must handle the missing shortcut.
func TestAnnotationsBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run annotation exports (needs Docker; task test:e2e)")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}
	// The default-provenance case must exercise Buildx's default rather
	// than inherit a caller's opt-out.
	t.Setenv("BUILDX_NO_DEFAULT_ATTESTATIONS", "false")
	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	localFrontend := e.buildFrontend()
	registry := e.startRegistry()
	frontend := registry + "/annotations-frontend:latest"
	e.run("docker", "tag", localFrontend, frontend)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", frontend).Run() })
	e.run("docker", "push", frontend)

	// The container driver preserves OCI annotations without depending
	// on the host engine's image store. An explicit builder also leaves
	// the user's selected builder unchanged.
	config := filepath.Join(t.TempDir(), "buildkitd.toml")
	require.NoError(t, os.WriteFile(config, fmt.Appendf(nil, "[registry.%q]\n  http = true\n", registry), 0o644))
	builder := fmt.Sprintf("kit-annotations-e2e-%d", time.Now().UnixNano())
	e.run("docker", "buildx", "create", "--name", builder, "--driver", "docker-container",
		"--driver-opt", "network=host", "--buildkitd-config", config)
	t.Cleanup(func() { _ = exec.Command("docker", "buildx", "rm", "--force", builder).Run() })
	e.run("docker", "buildx", "inspect", "--bootstrap", builder)
	t.Log(e.run("docker", "buildx", "version"))
	t.Log(e.run("docker", "buildx", "inspect", builder))

	dir := t.TempDir()
	descriptor := `# syntax=` + frontend + `
schemaVersion: "3"
kind: mixin
displayName: Exported annotations
description: Metadata survives the exporter.
version: "1.0.0"
author: Kit test
sourceUrl: https://example.com/kit
licenses: [MIT]
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [example.com]
build: |
  FROM scratch
  COPY payload /payload
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "annotations.yaml"), []byte(descriptor), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "payload"), []byte("exported payload\n"), 0o644))
	authored, err := spec.Decode([]byte(descriptor))
	require.NoError(t, err)

	for _, tt := range []struct {
		name             string
		platforms        []string
		flags            []string
		attestations     bool
		indexAnnotations bool
	}{
		{name: "single-default", platforms: []string{"linux/amd64"}, attestations: true},
		{name: "single-provenance", platforms: []string{"linux/amd64"}, flags: []string{"--provenance=mode=min"}, attestations: true, indexAnnotations: true},
		{name: "single-bare", platforms: []string{"linux/amd64"}, flags: []string{"--provenance=false"}},
		{name: "multi-default", platforms: []string{"linux/amd64", "linux/arm64"}, attestations: true, indexAnnotations: true},
		{name: "multi-bare", platforms: []string{"linux/amd64", "linux/arm64"}, flags: []string{"--provenance=false"}, indexAnnotations: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &e2e{t: t, builder: builder}
			args := []string{"buildx", "--builder", builder, "build", dir,
				"-f", filepath.Join(dir, "annotations.yaml"), "--push",
				"-t", registry + "/annotations:" + tt.name,
				"--platform", strings.Join(tt.platforms, ",")}
			t.Log(e.run("docker", append(args, tt.flags...)...))
			root := readExportedImage(t, registry, "annotations", tt.name)
			var manifests []ocispec.Descriptor
			if len(tt.platforms) > 1 || tt.attestations {
				require.Equal(t, ocispec.MediaTypeImageIndex, root.MediaType)
				require.NotEmpty(t, root.Manifests)
				manifests = root.Manifests
			} else {
				require.Equal(t, ocispec.MediaTypeImageManifest, root.MediaType,
					"a single platform without attestations has no image index to annotate")
			}

			images := []exportedImage{root}
			var platforms []string
			attestations := 0
			if len(manifests) > 0 {
				images = nil
				for _, manifest := range manifests {
					if manifest.Annotations["vnd.docker.reference.type"] == "attestation-manifest" {
						attestations++
						continue
					}
					require.NotNil(t, manifest.Platform)
					platforms = append(platforms, manifest.Platform.OS+"/"+manifest.Platform.Architecture)
					image := readExportedImage(t, registry, "annotations", manifest.Digest.String())
					require.Equal(t, ocispec.MediaTypeImageManifest, image.MediaType)
					images = append(images, image)
				}
				require.ElementsMatch(t, tt.platforms, platforms)
			}
			require.NotEmpty(t, images)
			raw := images[0].Annotations[spec.AnnotationDescriptor]
			require.NotEmpty(t, raw, "every platform image carries the descriptor")
			published, err := spec.Decode([]byte(raw))
			require.NoError(t, err)
			_, err = spec.ValidatePublished([]byte(raw), published)
			require.NoError(t, err)
			require.Equal(t, authored, published, "export preserves the descriptor's declarations")

			expected := map[string]string{
				spec.AnnotationDescriptor:              raw,
				spec.AnnotationSchemaVersion:           "3",
				spec.AnnotationCapabilities:            "com.docker.sandbox/network-policy@1",
				"org.opencontainers.image.title":       "Exported annotations",
				"org.opencontainers.image.description": "Metadata survives the exporter.",
				"org.opencontainers.image.version":     "1.0.0",
				"org.opencontainers.image.authors":     "Kit test",
				"org.opencontainers.image.source":      "https://example.com/kit",
				"org.opencontainers.image.licenses":    "MIT",
			}
			builtBy, present, err := spec.ParseBuiltBy(images[0].Annotations)
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, "docker/sandbox-kit", builtBy.Name)
			require.Equal(t, "dev", builtBy.Version)
			expected[spec.AnnotationBuiltBy] = images[0].Annotations[spec.AnnotationBuiltBy]
			for _, image := range images {
				require.Equal(t, expected, image.Annotations, "all platform manifests carry identical Kit and OCI annotations")
			}
			// Inline-only default provenance can be omitted by some
			// exporters and retained by others. The frontend cannot
			// promise an index annotation for that case, but must not
			// lose any platform metadata or require an index shortcut.
			if tt.indexAnnotations || root.Annotations[spec.AnnotationDescriptor] != "" {
				require.Equal(t, expected, root.Annotations, "index annotations agree with platform manifests")
			}
			for _, platform := range tt.platforms {
				osName, arch, _ := strings.Cut(platform, "/")
				client, err := fetch.New(fetch.WithPlainHTTP(), fetch.WithPlatform(ocispec.Platform{OS: osName, Architecture: arch}))
				require.NoError(t, err)
				kit, err := client.Fetch(t.Context(), registry+"/annotations:"+tt.name)
				require.NoError(t, err, "consumers resolve even an index without annotations")
				require.Equal(t, authored, kit.Descriptor)
			}
			if tt.attestations {
				require.Equal(t, len(tt.platforms), attestations)
			} else {
				require.Zero(t, attestations)
			}
		})
	}
	t.Run("oci-default", func(t *testing.T) {
		e := &e2e{t: t, builder: builder}
		layout := filepath.Join(t.TempDir(), "layout")
		t.Log(e.run("docker", "buildx", "--builder", builder, "build", dir,
			"-f", filepath.Join(dir, "annotations.yaml"), "--platform=linux/amd64",
			"-t", "annotations:oci", "--output=type=oci,dest="+layout+",tar=false"))
		catalogBytes, err := os.ReadFile(filepath.Join(layout, "index.json"))
		require.NoError(t, err)
		var catalog ocispec.Index
		require.NoError(t, json.Unmarshal(catalogBytes, &catalog))
		require.Len(t, catalog.Manifests, 1)
		imageDigest := catalog.Manifests[0].Digest
		imageBytes, err := os.ReadFile(filepath.Join(layout, "blobs", imageDigest.Algorithm().String(), imageDigest.Encoded()))
		require.NoError(t, err)
		var image ocispec.Manifest
		require.NoError(t, json.Unmarshal(imageBytes, &image))
		require.Equal(t, ocispec.MediaTypeImageManifest, image.MediaType,
			"default inline-only provenance must still allow an OCI bare-manifest export")
		published, err := spec.Decode([]byte(image.Annotations[spec.AnnotationDescriptor]))
		require.NoError(t, err)
		require.Equal(t, authored, published)
		require.Equal(t, "3", image.Annotations[spec.AnnotationSchemaVersion])
		require.Equal(t, "com.docker.sandbox/network-policy@1", image.Annotations[spec.AnnotationCapabilities])
		require.NotEmpty(t, image.Annotations[spec.AnnotationBuiltBy])
	})
}

// Read the tagged image index itself, without a client's automatic
// platform selection masking absent index annotations.
type exportedImage struct {
	MediaType   string               `json:"mediaType"`
	Annotations map[string]string    `json:"annotations"`
	Manifests   []ocispec.Descriptor `json:"manifests"`
}

func readExportedImage(t *testing.T, registry, repository, reference string) exportedImage {
	t.Helper()
	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", registry, repository, reference)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", strings.Join([]string{ocispec.MediaTypeImageIndex, ocispec.MediaTypeImageManifest}, ", "))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "fetch %s: %s", url, body)
	var image exportedImage
	require.NoError(t, json.Unmarshal(body, &image))
	return image
}
