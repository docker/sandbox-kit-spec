package sandbox

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These regressions drive real processes from Python so that signals and
// unreaped children behave as they do across fake-adapter invocations.
// Keep them in the Go suite: testdata is otherwise outside test discovery.
func TestOrderingFakeProcesses(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("testdata", "ordering-fake_test.py"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

func TestOrderingConformingRuntime(t *testing.T) {
	for _, c := range checks {
		if c.requirement != "SPEC-v3 §7.1.1/selection-before-application" && c.requirement != "SPEC-v3 §7.1.1/conflicts-before-application" && !strings.HasPrefix(c.requirement, "SPEC-v3 §7.1.3/") && !strings.HasPrefix(c.requirement, "SPEC-v3 §12/") && c.requirement != "lifecycle@1/hooks-dependency-order" && c.requirement != "lifecycle@1/hook-failure" {
			continue
		}
		t.Run(c.requirement, func(t *testing.T) {
			rep := runAgainstFake(t, "", c)
			require.False(t, rep.Failed(), "%s", rep)
			require.Empty(t, rep.Findings, "the conforming fake must exercise the check rather than skip it")
		})
	}
}
