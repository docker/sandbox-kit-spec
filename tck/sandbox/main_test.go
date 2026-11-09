package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Go's ./... discovery excludes testdata, but the relay's protocol parser
// must accept valid inputs as well as the suite's mutation probes.
func TestSSHRelayProtocol(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "test", "./testdata/ssh-relay")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

// TestMain builds the fake's Go helpers once. The relay verifies SSH
// signatures and the ordering helper uses the public planning API;
// rebuilding either per adapter invocation would multiply the suite's cost.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "kit-tck-relay-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	relay := filepath.Join(dir, "ssh-relay")
	build := exec.Command("go", "build", "-o", relay, "./testdata/ssh-relay")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build the fake's ssh-relay:", err)
		return 1
	}
	// Inherited by every fake-adapter run, whichever test builds the
	// adapter's environment.
	if err := os.Setenv("KIT_TCK_FAKE_RELAY", relay); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ordering := filepath.Join(dir, "ordering-runtime")
	buildOrdering := exec.Command("go", "build", "-o", ordering, "./testdata/ordering-runtime")
	buildOrdering.Stdout, buildOrdering.Stderr = os.Stderr, os.Stderr
	if err := buildOrdering.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build the fake's ordering planner:", err)
		return 1
	}
	if err := os.Setenv("KIT_TCK_FAKE_ORDERING", ordering); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return m.Run()
}
