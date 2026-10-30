package sandbox

import (
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"

	"github.com/stretchr/testify/require"
)

func TestHostPortDualUsesEitherFamily(t *testing.T) {
	rep := runAgainstFake(t, "host-port-dual-uses-ipv6", check{requirement: "host-port@1/family-honored", capability: capHostPort, run: hostPortFamily})
	require.False(t, rep.Failed(), "%s", rep)
}

func TestHostPortFamilyAndPublishedConflict(t *testing.T) {
	for _, c := range checks {
		if c.requirement != "host-port@1/family-honored" && c.requirement != "host-port@1/published-port-conflict" {
			continue
		}
		t.Run(c.requirement, func(t *testing.T) {
			rep := runAgainstFake(t, "", c)
			require.False(t, rep.Failed(), "%s", rep)
			for _, finding := range rep.Findings {
				require.NotEqual(t, report.Skip, finding.Severity, "%s", rep)
			}
		})
	}
}
