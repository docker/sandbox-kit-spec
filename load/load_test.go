package load_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SDKs import this contract to avoid compiling a registry client. Check the
// production dependency graph so a transitive import cannot undo that boundary.
func TestDependencies(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "github.com/docker/sandbox-kit-spec/v3/load")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.ElementsMatch(t, []string{
		"github.com/docker/sandbox-kit-spec/v3/load",
		"github.com/opencontainers/go-digest",
		"github.com/opencontainers/image-spec/specs-go",
		"github.com/opencontainers/image-spec/specs-go/v1",
	}, strings.Fields(string(output)))
}
