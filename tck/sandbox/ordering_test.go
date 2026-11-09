package sandbox

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrderingChecksUseTheAPIPlan(t *testing.T) {
	var selected []check
	for _, candidate := range checks {
		if strings.HasPrefix(candidate.requirement, "SPEC-v3 §7.1.2/") || candidate.requirement == "lifecycle@1/hooks-dependency-order" {
			selected = append(selected, candidate)
		}
	}
	require.Len(t, selected, 7)
	rep := runAgainstFake(t, "", selected...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestOrderingRefusalProbeRequiresWorkingTraceRoute(t *testing.T) {
	for _, candidate := range checks {
		if candidate.requirement == "SPEC-v3 §7.1.2/no-partial-application" {
			rep := runAgainstFake(t, "ordering-no-trace-mapping", candidate)
			require.True(t, rep.Failed(), "a refusal probe with no observable guest-to-host route must fail: %s", rep)
			return
		}
	}
	t.Fatal("no refusal ordering check")
}
