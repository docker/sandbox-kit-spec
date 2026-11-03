package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

func TestSecretsBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run secret builds (needs Docker)")
	}
	if out, err := exec.CommandContext(t.Context(), "docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}
	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	localFrontend := e.buildFrontend()
	registry := e.startRegistry()
	frontend := registry + "/secret-frontend:latest"
	e.run("docker", "tag", localFrontend, frontend)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", frontend).Run() })
	e.run("docker", "push", frontend)
	config := filepath.Join(t.TempDir(), "buildkitd.toml")
	require.NoError(t, os.WriteFile(config, fmt.Appendf(nil, "[registry.%q]\n  http = true\n", registry), 0o644))
	builder := fmt.Sprintf("kit-secrets-e2e-%d", time.Now().UnixNano())
	e.run("docker", "buildx", "create", "--name", builder, "--driver", "docker-container", "--driver-opt", "network=host", "--buildkitd-config", config)
	t.Cleanup(func() { _ = exec.Command("docker", "buildx", "rm", "--force", builder).Run() })
	e.run("docker", "buildx", "inspect", "--bootstrap", builder)

	// Keep the secret source outside every build context. Looking through all
	// exported blobs, including provenance and historical layers, catches more
	// than checking the final rootfs where a later RUN could have deleted it.
	token := "kit-e2e-private-token-" + fmt.Sprint(time.Now().UnixNano())
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte(token), 0o600))
	t.Setenv("KIT_E2E_SECRET", token)
	for _, tt := range []struct {
		name, kind  string
		inline, env bool
	}{
		{name: "companion-workload", kind: "workload"},
		{name: "inline-workload", kind: "workload", inline: true, env: true},
		{name: "companion-mixin", kind: "mixin"},
		{name: "inline-mixin", kind: "mixin", inline: true, env: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := &e2e{t: t, builder: builder}
			dir := t.TempDir()
			recipe := "FROM busybox:1.37\nRUN --mount=type=secret,id=TOKEN,required=true test -s /run/secrets/TOKEN && echo installed > /private-dependency\nRUN test ! -e /run/secrets/TOKEN\n"
			if tt.kind == "workload" {
				recipe += "ENTRYPOINT [\"/bin/sh\"]\n"
			}
			descriptor := fmt.Sprintf("# syntax=%s\nschemaVersion: \"3\"\nkind: %s\nprovides: [private-deps@1.0.0]\nargs:\n  token: {buildSecret: TOKEN}\n", frontend, tt.kind)
			if tt.inline {
				descriptor += "build: |\n  " + strings.ReplaceAll(strings.TrimSuffix(recipe, "\n"), "\n", "\n  ") + "\n"
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "private.dockerfile"), []byte(recipe), 0o644))
			}
			file := filepath.Join(dir, "private.yaml")
			require.NoError(t, os.WriteFile(file, []byte(descriptor), 0o644))
			base := []string{"build", "--builder", builder, dir, "-f", file, "--progress=plain", "--no-cache"}
			// A missing secret must fail at the required mount. Ordinary build args
			// must fail before the recipe, with no supplied bytes in diagnostics.
			cmd := exec.CommandContext(t.Context(), "docker", append(base, "--output=type=cacheonly")...)
			out, err := cmd.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(out), "secret TOKEN")
			cmd = exec.CommandContext(t.Context(), "docker", append(base, "--build-arg", "token="+token, "--output=type=cacheonly")...)
			out, err = cmd.CombinedOutput()
			require.Error(t, err)
			require.Contains(t, string(out), "use --secret id=TOKEN")
			require.NotContains(t, string(out), token)

			layout := filepath.Join(t.TempDir(), "layout")
			source := "src=" + tokenFile
			if tt.env {
				source = "env=KIT_E2E_SECRET"
			}
			output := e.run("docker", append(base, "--secret", "id=TOKEN,"+source, "--provenance=mode=max", "-t", "secret-test:"+tt.name, "--output=type=oci,dest="+layout+",tar=false")...)
			require.NotContains(t, output, token)
			artifact, err := tckkit.FromLayout(t.Context(), layout, tt.name)
			require.NoError(t, err)
			installed, present, err := artifact.ReadFile(t.Context(), "/private-dependency")
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, "installed\n", string(installed))
			_, present, err = artifact.ReadFile(t.Context(), "/run/secrets/TOKEN")
			require.NoError(t, err)
			require.False(t, present)
			published, err := spec.Decode([]byte(artifact.Annotations()[spec.AnnotationDescriptor]))
			require.NoError(t, err)
			require.Equal(t, "TOKEN", published.Args["token"].BuildSecret)
			values, err := spec.ResolveArgs(published.Args, nil)
			require.NoError(t, err)
			require.Empty(t, values)
			// Busybox deliberately lacks the sandbox platform floor, so this is a
			// frontend/transport test rather than a claim of runtime conformance.
			provenance := false
			layers := 0
			require.NoError(t, filepath.WalkDir(layout, func(name string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				data, err := os.ReadFile(name)
				if err != nil {
					return err
				}
				require.NotContains(t, string(data), token, "exported blob %s", name)
				provenance = provenance || bytes.Contains(data, []byte("https://slsa.dev/provenance/"))
				if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
					layers++
					reader, err := gzip.NewReader(bytes.NewReader(data))
					require.NoError(t, err)
					plain, err := io.ReadAll(reader)
					require.NoError(t, err)
					require.NoError(t, reader.Close())
					require.NotContains(t, string(plain), token, "exported layer %s", name)
				}
				return nil
			}))
			require.True(t, provenance, "the exporter must include provenance for this test to judge")
			require.Positive(t, layers, "exported layers must be inspected after decompression")
		})
	}
}
