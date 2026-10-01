package main

import (
	"context"
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

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/assemble"
	"github.com/docker/sandbox-kit-spec/v3/fetch"
	"github.com/docker/sandbox-kit-spec/v3/spec"
	tckkit "github.com/docker/sandbox-kit-spec/v3/tck/kit"
)

// The set path is the one part of the frontend that unit tests cannot
// reach: resolving a kit, reading the descriptor it staged, and merging
// layers all need a gateway client and a registry to pull from. This
// test supplies both — it publishes two kits to a throwaway registry,
// builds a set over them, and reads the merged descriptor back off the
// manifest.
//
// Opt-in, because it needs a working Docker with buildx and pulls
// busybox and registry:2. `task test:e2e` runs it; the ordinary suite
// skips it. It stays compiled either way, so it cannot rot unnoticed
// behind a build tag.
func TestSetBuildsEndToEnd(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run the end-to-end set build (needs Docker; task test:e2e)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not on PATH")
	}
	// The daemon, not just the client: everything below builds, pushes,
	// and runs, so a stopped Docker turns the whole test into one
	// failure about a missing socket. Asking first is the difference
	// between "not run here" and "broken".
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}

	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	frontend := e.buildFrontend()
	registry := e.startRegistry()

	dir := t.TempDir()
	// A workload with a policy, a profile, and a license of its own.
	e.publish(dir, "base", registry+"/sbx-kit-base:1.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
displayName: E2E Base
kind: workload
version: "1.0.0"
provides: ["e2e-base@1.0.0"]
licenses: [Apache-2.0]
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [base.example.com]
        deny: [evil.example.com]
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: AGENTS.md
      content: "Base guidance."
`, map[string]string{"base.dockerfile": "FROM busybox:1.37\n" +
		// A dpkg database of one package, so §9.6 derivation actually
		// runs here. busybox carries none, and without it the set path
		// below would have nothing to double up.
		"RUN mkdir -p /var/lib/dpkg && printf 'Package: e2e-pkg\\nStatus: install ok installed\\nVersion: 1:2.3.4-5+e2e1\\n\\n' > /var/lib/dpkg/status\n" +
		"ENTRYPOINT [\"/bin/sh\"]\n"})

	// A mixin that requires the workload, adds content and egress of its
	// own, and takes a create-phase arg the set will supply.
	e.publish(dir, "extra", registry+"/sbx-kit-extra:2.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
displayName: E2E Extra
kind: mixin
version: "2.0.0"
provides: ["e2e-extra@2.0.0"]
requires: ["e2e-base >= 1.0.0"]
licenses: [MIT]
args:
  flavor:
    default: plain
    pattern: '^[a-z]+$'
capabilities:
  - type: com.docker.sandbox/network-policy@1
    config:
      runtime:
        allow: [extra.example.com]
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: CLAUDE.md
      directory: /home/agent/.claude
      content: "Extra is installed."
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - {path: /etc/extra.conf, content: "flavor=${{ kit.args.flavor }}"}
build: |
  FROM busybox:1.37 AS b
  RUN mkdir -p /out/opt && echo extra > /out/opt/extra
  FROM scratch
  COPY --from=b /out/ /
`, nil)

	// The set: both kits, with its own arg re-exported into one of them.
	e.publish(dir, "team", registry+"/sbx-kit-team:3.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
displayName: E2E Team
kind: set
version: "3.0.0"
provides: ["e2e-team@3.0.0"]
licenses: [BSD-3-Clause]
args:
  flavor:
    default: spicy
    pattern: '^[a-z]+$'
    buildArg: FLAVOR
  language:
    default: en
    buildArg: LANGUAGE
capabilities:
  - type: com.docker.sandbox/agent-context@1
    config:
      filename: CLAUDE.md
      directory: /home/agent/.claude
      contentFile: ./notes-${{ kit.args.language }}.md
kits:
  - ref: `+registry+`/sbx-kit-base:1.0.0
  - ref: `+registry+`/sbx-kit-extra:2.0.0
    args:
      flavor: ${{ kit.args.flavor }}
`, map[string]string{"notes-en.md": "Team guidance."})

	// The workload derived its own filesystem's package, epoch and
	// revision removed, before any of the set machinery ran.
	baseRaw := e.manifest(registry, "sbx-kit-base", "1.0.0").Annotations[spec.AnnotationDescriptor]
	baseDescriptor, err := spec.Decode([]byte(baseRaw))
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"e2e-base@1.0.0", "deb/e2e-pkg@2.3.4"}, baseDescriptor.Provides,
		"a workload states what its own package database records")

	manifest := e.manifest(registry, "sbx-kit-team", "3.0.0")
	raw := manifest.Annotations[spec.AnnotationDescriptor]
	require.NotEmpty(t, raw, "the merged artifact carries a kit descriptor")

	d, err := spec.Decode([]byte(raw))
	require.NoError(t, err)
	_, err = spec.ValidatePublished([]byte(raw), d)
	require.NoError(t, err, "a merged set is a published kit like any other")

	require.Equal(t, spec.KindWorkload, d.Kind, "a workload among its kits makes the merge a workload")
	// The derived entry arrives through the provides union, once. A set
	// merges to kind: workload, so a derivation keyed on kind alone would
	// read the merged filesystem and append a second copy of everything
	// the base already contributed (§9.6: a set carries, never re-derives).
	require.ElementsMatch(t,
		[]string{"e2e-base@1.0.0", "e2e-extra@2.0.0", "e2e-team@3.0.0", "deb/e2e-pkg@2.3.4"},
		d.Provides, "a set carries its kits' derived entries through, exactly once")
	require.Empty(t, d.Requires, "the set answers e2e-base itself, so the merged kit does not require it")
	require.Equal(t, []string{"Apache-2.0", "BSD-3-Clause", "MIT"}, d.Licenses)

	policy, err := spec.NetworkPolicyOf(d.Capabilities)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"base.example.com", "extra.example.com"}, policy.Runtime.Allow)
	require.Equal(t, []string{"evil.example.com"}, policy.Runtime.Deny,
		"a deny survives from whichever kit stated it")

	// The re-exported arg: the set's build-phase value reaches the other
	// kit's hook text, which is the regression this test exists for.
	lifecycle, err := spec.LifecycleOf(d.Capabilities)
	require.NoError(t, err)
	require.Len(t, lifecycle.Files, 1)
	require.Equal(t, "flavor=spicy", lifecycle.Files[0].Content)

	context, err := spec.AgentContextOf(d.Capabilities)
	require.NoError(t, err)
	require.Equal(t, "CLAUDE.md", context.Filename,
		"the agent mixin and set agree on the explicit profile overriding the base's legacy filename")
	require.Equal(t, "/home/agent/.claude", context.Directory)
	require.Equal(t, "/usr/share/sandbox/kit/team/context.md", context.ContentFile)

	require.Len(t, d.Kits, 2)
	for _, k := range d.Kits {
		require.Regexp(t, `^sha256:[0-9a-f]{64}$`, k.Digest, "%s is pinned to what the build resolved", k.Ref)
	}
	require.Equal(t, map[string]string{"flavor": "spicy"}, d.Kits[1].Args,
		"the recorded value is the one that was merged in, not the reference it was written as")

	// The merged filesystem carries every kit's staged sources and the
	// concatenated context body.
	out := e.run("docker", "run", "--rm", "--pull=always", registry+"/sbx-kit-team:3.0.0",
		"-c", "ls /usr/share/sandbox/kit/ && cat /usr/share/sandbox/kit/team/context.md && cat /opt/extra")
	for _, want := range []string{"base", "extra", "team", "Base guidance.", "Extra is installed.", "Team guidance."} {
		require.Contains(t, out, want)
	}

	// And the published artifact answers to the conformance suite, read
	// back out of the registry rather than out of the build: what an
	// exporter and a registry did to a merged set is the part no
	// build-time check can see.
	artifacts, err := tckkit.FromRegistryAll(t.Context(), registry+"/sbx-kit-team:3.0.0")
	require.NoError(t, err)
	require.NotEmpty(t, artifacts)
	for _, artifact := range artifacts {
		report, err := tckkit.Run(t.Context(), artifact)
		require.NoError(t, err)
		require.NoError(t, report.Err(), "the published set conforms:\n%s", report)
	}
	t.Run("final environment", func(t *testing.T) {
		child := &e2e{t: t, builder: e.builder}
		child.environmentSet(frontend, registry)
	})
	t.Run("conditional declarations", func(t *testing.T) {
		child := &e2e{t: t, builder: e.builder}
		child.groupSet(frontend, registry)
	})
}

// e2e runs the docker commands the test needs, failing the test on the
// first one that does not succeed.
type e2e struct {
	t       *testing.T
	builder string
}

// dockerDriverBuilder names the builder to build through: the one
// backed by the daemon itself.
//
// It has to be the docker driver, twice over. The frontend under test
// exists only in the local image store, which a container driver cannot
// read, and the registry is published on the host, which a container
// driver cannot reach. Buildx names that builder after the current
// docker context and refuses one belonging to another context, so the
// context is where the name comes from.
func dockerDriverBuilder(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "context", "show").Output()
	if err != nil {
		return "default"
	}
	if name := strings.TrimSpace(string(out)); name != "" {
		return name
	}
	return "default"
}

func (e *e2e) run(name string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	require.NoError(e.t, err, "%s %s\n%s", name, strings.Join(args, " "), out)
	return string(out)
}

// buildFrontend builds the frontend under a tag unique to this run.
// BuildKit caches frontend resolution per reference, so a reused tag can
// keep dispatching a stale binary no matter how often it is rebuilt.
func (e *e2e) buildFrontend() string {
	e.t.Helper()
	tag := fmt.Sprintf("docker/sandbox-kit:e2e-%d", time.Now().UnixNano())
	e.run("docker", "build", "-q", "-t", tag, "../..")
	e.t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", tag).Run() })
	return tag
}

// startRegistry runs a throwaway registry and returns its host address.
// The host port is assigned rather than fixed, so concurrent runs and an
// already-occupied port cannot collide.
func (e *e2e) startRegistry() string {
	e.t.Helper()
	name := fmt.Sprintf("kit-e2e-registry-%d", time.Now().UnixNano())
	e.run("docker", "run", "-d", "--name", name, "-p", "0:5000", "registry:2")
	e.t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	published := strings.TrimSpace(e.run("docker", "port", name, "5000/tcp"))
	// docker port may report one line per address family.
	line := strings.TrimSpace(strings.Split(published, "\n")[0])
	port := line[strings.LastIndex(line, ":")+1:]
	address := "localhost:" + port

	// The registry answers /v2/ as soon as it is listening; the push
	// below would otherwise race the container's startup.
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get("http://" + address + "/v2/")
		if err == nil {
			_ = resp.Body.Close()
			return address
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("registry at %s never came up: %v", address, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// publish writes a kit's sources and builds it straight into the
// registry. The builder is named explicitly: the docker driver is the
// one that can see the locally built frontend image and reach a
// registry published on the host, and the current builder may be
// neither.
func (e *e2e) publish(root, stem, ref, descriptor string, extra map[string]string) {
	e.t.Helper()
	dir := filepath.Join(root, stem)
	require.NoError(e.t, os.MkdirAll(dir, 0o755))
	require.NoError(e.t, os.WriteFile(filepath.Join(dir, stem+".yaml"), []byte(descriptor), 0o644))
	for name, body := range extra {
		require.NoError(e.t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(e.t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	e.run("docker", "buildx", "--builder", e.builder, "build", dir,
		"-f", filepath.Join(dir, stem+".yaml"),
		"--push", "-t", ref, "--provenance=false")
}

// publishedManifest is the part of a pushed manifest this test reads.
type publishedManifest struct {
	Annotations map[string]string `json:"annotations"`
	Layers      []any             `json:"layers"`
}

// manifest fetches a pushed manifest, which is where the annotations the
// merge produced actually live.
func (e *e2e) manifest(registry, repository, tag string) publishedManifest {
	e.t.Helper()
	var out publishedManifest
	url := fmt.Sprintf("http://%s/v2/%s/manifests/%s", registry, repository, tag)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(e.t, err)
	req.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(e.t, err)
	require.Equal(e.t, http.StatusOK, resp.StatusCode, "fetch %s: %s", url, body)
	require.NoError(e.t, json.Unmarshal(body, &out))
	return out
}

// Exercise real layer staging, publication, fetch-time selection and assembly
// together: neither a schema test nor a merge unit test can see missing bodies.
func (e *e2e) groupSet(frontend, registry string) {
	t := e.t
	dir := t.TempDir()
	e.publish(dir, "conditional", registry+"/sbx-kit-conditional:1.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      startup: [{command: before}]
  - type: com.docker.sandbox/agent-context@1
    config: {contentFile: ./always/context.md}
  - group:
      name: Optional feature
      optional: true
      capabilities:
        - type: com.example/feature@1
        - type: com.docker.sandbox/lifecycle@1
          config:
            startup: [{command: inside}]
        - type: com.docker.sandbox/agent-context@1
          config: {contentFile: ./optional/context.md}
`, map[string]string{"always/context.md": "Always available.", "optional/context.md": "Only for the optional feature."})
	e.publish(dir, "conditional-set", registry+"/sbx-kit-conditional-set:1.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
kind: set
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      startup: [{command: after}]
  - type: com.docker.sandbox/agent-context@1
    config: {contentFile: ./own.md}
kits:
  - ref: `+registry+`/sbx-kit-base:1.0.0
  - ref: `+registry+`/sbx-kit-conditional:1.0.0
`, map[string]string{"own.md": "Set guidance."})
	ref := registry + "/sbx-kit-conditional-set:1.0.0"
	client, err := fetch.New(fetch.WithPlainHTTP())
	require.NoError(t, err)
	published, err := client.Fetch(t.Context(), ref)
	require.NoError(t, err)
	require.True(t, spec.HasGroups(published.Descriptor.Capabilities))
	var bodies []string
	for _, entry := range spec.DeclaredCapabilities(published.Descriptor.Capabilities) {
		if entry.Type == spec.CapabilityAgentContext {
			var ac spec.AgentContext
			require.NoError(t, spec.DecodeCapabilityConfig(entry, &ac))
			bodies = append(bodies, ac.ContentFile)
		}
	}
	require.Len(t, bodies, 4)
	args := append([]string{"run", "--rm", "--pull=always", "--entrypoint", "cat", ref}, bodies...)
	text := e.run("docker", args...)
	for _, body := range []string{"Base guidance.", "Always available.", "Only for the optional feature.", "Set guidance."} {
		require.Contains(t, text, body)
	}
	for _, accept := range []bool{false, true} {
		calls := 0
		resolved, err := client.Resolve(t.Context(), []fetch.Request{{Reference: ref}}, fetch.WithCapabilitySelector(func(_ context.Context, _ spec.Descriptor, c spec.Capability) spec.CapabilityDecision {
			calls++
			return spec.CapabilityDecision{Accepted: accept || c.Type != "com.example/feature@1"}
		}))
		require.NoError(t, err)
		lc, err := spec.LifecycleOf(resolved.Descriptor.Capabilities)
		require.NoError(t, err)
		var commands []string
		for _, hook := range lc.Startup {
			commands = append(commands, strings.Join(hook.Command, " "))
		}
		expected := []string{"sh -c before", "sh -c after"}
		if accept {
			expected = []string{"sh -c before", "sh -c inside", "sh -c after"}
		}
		require.Equal(t, expected, commands)
		selectedBodies := 0
		for _, kit := range resolved.Kits {
			for _, c := range kit.Descriptor.Capabilities {
				if c.Type == spec.CapabilityAgentContext {
					selectedBodies++
				}
			}
		}
		expectedBodies := 3
		if accept {
			expectedBodies = 4
		}
		require.Equal(t, expectedBodies, selectedBodies)
		before := calls
		_, err = assemble.Assemble(t.Context(), resolved.Kits, client.LoadImage)
		require.NoError(t, err)
		require.Equal(t, before, calls)
	}
	artifacts, err := tckkit.FromRegistryAll(t.Context(), ref)
	require.NoError(t, err)
	for _, artifact := range artifacts {
		rep, err := tckkit.Run(t.Context(), artifact)
		require.NoError(t, err)
		require.NoError(t, rep.Err(), "%s", rep)
	}
}

func (e *e2e) environmentSet(frontend, registry string) {
	t := e.t
	dir := t.TempDir()
	e.publish(dir, "environment", registry+"/sbx-kit-environment:1.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
kind: mixin
capabilities:
  - type: com.docker.sandbox/lifecycle@1
    config:
      files:
        - path: '${{ kit.env.HOME }}/config'
          content: '${{ kit.env.MESSAGE }}'
  - type: com.docker.sandbox/agent-context@1
    config:
      content: 'Home: ${{ kit.env.HOME }}'
`, nil)
	e.publish(dir, "environment-set", registry+"/sbx-kit-environment-set:1.0.0", `# syntax=`+frontend+`
schemaVersion: "3"
kind: set
kits:
  - ref: `+registry+`/sbx-kit-base:1.0.0
  - ref: `+registry+`/sbx-kit-environment:1.0.0
`, nil)
	client, err := fetch.New(fetch.WithPlainHTTP())
	require.NoError(t, err)
	result, err := fetch.Assemble(t.Context(), []fetch.Request{{Reference: registry + "/sbx-kit-environment-set:1.0.0"}}, fetch.Options{
		LayerValidator: fetch.DefaultLayerValidator,
		Loader:         client.LoadKit,
		Overrides:      fetch.Overrides{Env: map[string]string{"HOME": "/home/runtime", "MESSAGE": "configured"}},
	})
	require.NoError(t, err)
	lc, err := spec.LifecycleOf(result.Resolved.Descriptor.Capabilities)
	require.NoError(t, err)
	require.Len(t, lc.Files, 1)
	require.Equal(t, "/home/runtime/config", lc.Files[0].Path)
	require.Equal(t, "configured", lc.Files[0].Content)
	found := false
	for _, c := range result.Resolved.Kits[0].Descriptor.Capabilities {
		if c.Type == spec.CapabilityAgentContext && c.Config["content"] == "Home: /home/runtime" {
			found = true
		}
	}
	require.True(t, found, "inline environment references survive set publication")
}
