package sandbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
