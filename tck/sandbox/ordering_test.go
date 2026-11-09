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
