package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// The frontend receives neither the credentials nor the CA. The kit's
// hostname cannot resolve outside the builder's mirror configuration.
// Reading its descriptor therefore exercises the same registry settings
// as the image pull, including authentication through the build session.
func (e *e2e) privateRegistrySet(localFrontend, registry string) {
	t := e.t
	dir := t.TempDir()
	target, err := url.Parse("http://" + registry)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	const username, password = "kit-e2e", "kit-e2e-password"
	var authenticated sync.Map
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != username || pass != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="kit-e2e"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Store(r.Method+" "+r.URL.Path, true)
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()

	// Even a client trusting the test CA needs registry credentials.
	response, err := server.Client().Get(server.URL + "/v2/")
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	mirror := strings.TrimPrefix(server.URL, "https://")
	const privateRegistry = "kit-registry.invalid"
	cert := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE", Bytes: server.Certificate().Raw,
	}), 0o644))
	config := filepath.Join(dir, "buildkitd.toml")
	require.NoError(t, os.WriteFile(config, fmt.Appendf(nil,
		"[registry.%q]\n  http = true\n[registry.%q]\n  mirrors = [%q]\n[registry.%q]\n  http = false\n  ca = [%q]\n",
		registry, privateRegistry, mirror, mirror, cert), 0o644))

	frontend := registry + "/private-registry-frontend:latest"
	e.run("docker", "tag", localFrontend, frontend)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", frontend).Run() })
	e.run("docker", "push", frontend)

	// An isolated client config supplies credentials only through the
	// session. It never changes the user's docker login or builders.
	clientConfig := filepath.Join(dir, "docker")
	require.NoError(t, os.MkdirAll(clientConfig, 0o700))
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	credentials, err := json.Marshal(map[string]any{"auths": map[string]any{
		privateRegistry: map[string]string{"auth": auth},
		mirror:          map[string]string{"auth": auth},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(clientConfig, "config.json"), credentials, 0o600))
	builder := fmt.Sprintf("kit-private-e2e-%d", time.Now().UnixNano())
	private := &e2e{t: t, builder: builder, dockerConfig: clientConfig}
	args := []string{"buildx", "create", "--name", builder, "--driver", "docker-container",
		"--driver-opt", "network=host", "--buildkitd-config", config}
	if image := os.Getenv("KIT_E2E_BUILDKIT_IMAGE"); image != "" {
		args = append(args, "--driver-opt", "image="+image)
	}
	private.run("docker", args...)
	t.Cleanup(func() {
		_ = exec.Command("docker", "--config", clientConfig, "buildx", "rm", "--force", builder).Run()
	})
	private.run("docker", "buildx", "inspect", "--bootstrap", builder)

	e.publish(dir, "private-base", registry+"/sbx-kit-private-base:1.0.0", `# syntax=`+localFrontend+`
schemaVersion: "3"
kind: workload
provides: [private-base@1.0.0]
build: |
  FROM busybox:1.37
  COPY payload /inherited.txt
  ENTRYPOINT ["/bin/sh"]
`, map[string]string{"payload": "Inherited payload."})

	for _, indexed := range []bool{false, true} {
		name := "private-manifest"
		var flags []string
		if indexed {
			name = "private-index"
			flags = []string{"--provenance=true"}
		}
		e.publish(dir, name, registry+"/sbx-kit-"+name+":1.0.0", `# syntax=`+localFrontend+`
schemaVersion: "3"
kind: workload
provides: [private-derived@1.0.0]
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [private.example.com]
build: |
  FROM `+registry+`/sbx-kit-private-base:1.0.0
`, nil, flags...)
		result := registry + "/sbx-kit-" + name + "-set:1.0.0"
		private.publish(dir, name+"-set", result, `# syntax=`+frontend+`
schemaVersion: "3"
kind: set
kits:
  - ref: `+privateRegistry+`/sbx-kit-`+name+`:1.0.0
`, nil)
		manifest := e.manifest(registry, "sbx-kit-"+name+"-set", "1.0.0")
		d, err := spec.Decode([]byte(manifest.Annotations[spec.AnnotationDescriptor]))
		require.NoError(t, err)
		require.Contains(t, d.Provides, "private-derived@1.0.0")
		require.NotContains(t, d.Provides, "private-base@1.0.0")
		require.Len(t, d.Kits, 1)
		_, ok := authenticated.Load("HEAD /v2/sbx-kit-" + name + "/blobs/" + d.Kits[0].Digest)
		if !ok {
			authenticated.Range(func(key, _ any) bool { t.Logf("authenticated request: %s", key); return true })
		}
		require.True(t, ok, "the builder's manifest blob lookup authenticated through its TLS mirror (pin %s)", d.Kits[0].Digest)

		artifact, err := tckkit.FromRegistry(t.Context(), result)
		require.NoError(t, err)
		for _, file := range []string{
			"/inherited.txt",
			"/usr/share/sandbox/kit/private-base/kit.yaml",
			"/usr/share/sandbox/kit/" + name + "/kit.yaml",
		} {
			_, exists, err := artifact.ReadFile(t.Context(), file)
			require.NoError(t, err)
			require.True(t, exists, "%s survives private-registry composition", file)
		}
	}
}
