package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

func TestCacheBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run the cache build (needs Docker)")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}

	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	localFrontend := e.buildFrontend()
	registry := e.startRegistry()
	frontend := registry + "/cache-frontend:latest"
	e.run("docker", "tag", localFrontend, frontend)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", frontend).Run() })
	e.run("docker", "push", frontend)

	// Separate builders cannot share local cache. Both use the container
	// driver because the docker driver cannot export registry cache with
	// the classic image store. Host networking lets them reach the
	// registry published on the Docker host.
	config := filepath.Join(t.TempDir(), "buildkitd.toml")
	require.NoError(t, os.WriteFile(config, fmt.Appendf(nil, "[registry.%q]\n  http = true\n", registry), 0o644))
	newBuilder := func() string {
		name := fmt.Sprintf("kit-cache-e2e-%d", time.Now().UnixNano())
		e.run("docker", "buildx", "create", "--name", name, "--driver", "docker-container",
			"--driver-opt", "network=host", "--buildkitd-config", config)
		t.Cleanup(func() { _ = exec.Command("docker", "buildx", "rm", "--force", name).Run() })
		e.run("docker", "buildx", "inspect", "--bootstrap", name)
		return name
	}
	exportBuilder, importBuilder := newBuilder(), newBuilder()

	for _, tt := range []struct {
		name   string
		kind   string
		before string
		after  string
	}{
		{name: "image-base", kind: "mixin", before: "FROM busybox:1.37\n"},
		{name: "stage-base", kind: "mixin", before: "FROM busybox:1.37 AS base\nRUN head -c 32 /dev/urandom > /base-marker\nFROM base\n"},
		{name: "scratch", kind: "mixin", before: "FROM busybox:1.37 AS build\n", after: "FROM scratch\nCOPY --from=build /kit-marker /kit-marker\n"},
		{name: "workload", kind: "workload", before: "FROM busybox:1.37\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &e2e{t: t, builder: exportBuilder}
			dir := t.TempDir()
			// Distinct RUN vertices also prevent earlier subtests or
			// previous runs from supplying the cache hit being judged.
			run := fmt.Sprintf("echo cache-%s-%d > /kit-marker", tt.name, time.Now().UnixNano())
			recipe := tt.before + "RUN " + run + "\n" + tt.after
			descriptor := fmt.Sprintf("# syntax=%s\nschemaVersion: \"3\"\nkind: %s\nprovides: [\"cache-test@1.0.0\"]\n", frontend, tt.kind)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "cache.yaml"), []byte(descriptor), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "cache.dockerfile"), []byte(recipe), 0o644))
			cache := registry + "/cache:" + tt.name
			e.run("docker", "buildx", "--builder", e.builder, "build", dir,
				"-f", filepath.Join(dir, "cache.yaml"), "--output=type=cacheonly",
				"--cache-to=type=registry,ref="+cache+",mode=max,image-manifest=true,oci-mediatypes=true")
			args := []string{"buildx", "--builder", importBuilder, "build", dir,
				"-f", filepath.Join(dir, "cache.yaml"), "--progress=rawjson", "--cache-from=type=registry,ref=" + cache}
			out := e.run("docker", append(args, "--output=type=cacheonly")...)
			require.Contains(t, out, "importing cache manifest from "+cache)
			requireRunCached(t, out, run, true)

			if tt.name == "stage-base" {
				// Eager evaluation must still honor --no-cache without
				// rebuilding the nondeterministic base a second time for
				// the lower side of the diff and leaking it into the kit.
				ref := registry + "/cache-no-cache:latest"
				out = e.run("docker", append(args, "--no-cache", "--push", "-t", ref, "--provenance=false")...)
				requireRunCached(t, out, run, false)
				artifact, err := tckkit.FromRegistry(t.Context(), ref)
				require.NoError(t, err)
				_, present, err := artifact.ReadFile(t.Context(), "/base-marker")
				require.NoError(t, err)
				require.False(t, present, "the lower stage must reuse the upper solve's fresh base")
			}
		})
	}
}

func requireRunCached(t *testing.T, progress, run string, want bool) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(progress))
	found := false
	cached := false
	for {
		var status struct {
			Vertexes []struct {
				Name      string
				Cached    bool
				Completed *time.Time
			}
		}
		err := decoder.Decode(&status)
		if err == io.EOF {
			break
		}
		require.NoError(t, err, "%s", progress)
		for _, vertex := range status.Vertexes {
			if strings.HasSuffix(vertex.Name, "RUN "+run) && vertex.Completed != nil {
				found = true
				// Later updates for extracting a cached snapshot do not
				// carry the cache-hit flag from the solve itself.
				cached = cached || vertex.Cached
			}
		}
	}
	require.True(t, found, "no completed RUN %s in progress:\n%s", run, progress)
	require.Equal(t, want, cached, "RUN %s\n%s", run, progress)
}
