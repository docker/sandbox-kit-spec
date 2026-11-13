package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// A caller redirects a companion's base the way it would any Dockerfile's,
// with --build-context; this is how a client points the images a kit
// builds on at a registry mirror without editing the recipe. BuildKit
// refuses the build before this frontend runs unless the frontend image
// declares the contexts capability, so only an end-to-end build sees it.
func TestNamedContextsReachCompanion(t *testing.T) {
	if os.Getenv("KIT_E2E") == "" {
		t.Skip("set KIT_E2E=1 to run the named-context build (needs Docker; task test:e2e)")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output(); err != nil || len(out) == 0 {
		t.Skip("the docker daemon is not reachable")
	}

	e := &e2e{t: t, builder: dockerDriverBuilder(t)}
	frontend := e.buildFrontend()
	registry := e.startRegistry()

	base := registry + "/context-base:1"
	e.run("docker", "pull", "-q", "busybox:1.37")
	e.run("docker", "tag", "busybox:1.37", base)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", base).Run() })
	e.run("docker", "push", "-q", base)

	dir := t.TempDir()
	descriptor := "# syntax=" + frontend + "\nschemaVersion: \"3\"\nkind: mixin\nprovides: [\"context-test@1.0.0\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "context.yaml"), []byte(descriptor), 0o644))
	// No registry serves this name: the build succeeds only through the
	// named context.
	recipe := "FROM sbx-kit-spec-e2e-unpublished:1\nRUN test -x /bin/busybox\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "context.dockerfile"), []byte(recipe), 0o644))

	e.run("docker", "buildx", "--builder", e.builder, "build", dir,
		"-f", filepath.Join(dir, "context.yaml"), "--output=type=cacheonly",
		"--build-context", "sbx-kit-spec-e2e-unpublished:1=docker-image://"+base)
}
