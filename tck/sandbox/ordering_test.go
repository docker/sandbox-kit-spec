package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func TestOrderingChecksUseTheAPIPlan(t *testing.T) {
	var selected []check
	for _, candidate := range checks {
		if strings.HasPrefix(candidate.requirement, "SPEC-v3 §7.1.2/") || candidate.requirement == "SPEC-v3 §7.1.1/selection-before-application" || candidate.requirement == "SPEC-v3 §7.1.1/conflicts-before-application" || candidate.requirement == "lifecycle@1/hooks-dependency-order" {
			selected = append(selected, candidate)
		}
	}
	require.Len(t, selected, 9)
	rep := runAgainstFake(t, "", selected...)
	require.False(t, rep.Failed(), "%s", rep)
}

func TestOrderingRefusalProbeRequiresWorkingTraceRoute(t *testing.T) {
	for _, requirement := range []string{
		"SPEC-v3 §7.1.2/no-partial-application",
		"SPEC-v3 §7.1.1/selection-before-application",
		"SPEC-v3 §7.1.1/conflicts-before-application",
	} {
		t.Run(requirement, func(t *testing.T) {
			for _, candidate := range checks {
				if candidate.requirement == requirement {
					rep := runAgainstFake(t, "ordering-no-trace-mapping", candidate)
					require.True(t, rep.Failed(), "a refusal probe with no observable guest-to-host route must fail: %s", rep)
					return
				}
			}
			t.Fatal("no refusal ordering check")
		})
	}
}

func TestOrderingInstallCannotObserveLifecycleFiles(t *testing.T) {
	for _, candidate := range checks {
		if candidate.requirement == "SPEC-v3 §7.1.2/numeric-order" {
			rep := runAgainstFake(t, "ordering-files-before-install", candidate)
			require.True(t, rep.Failed(), "writing lifecycle files before install must fail: %s", rep)
			return
		}
	}
	t.Fatal("no numeric ordering check")
}

func TestOrderingPrerequisitesRequireHostPortClaim(t *testing.T) {
	for _, candidate := range checks {
		if candidate.requirement == "SPEC-v3 §7.1.2/prerequisites" {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{
				"KIT_TCK_FAKE_STATE=" + t.TempDir(),
				"KIT_TCK_FAKE_CLAIMS=" + capDockerd + "," + capLifecycle,
				"KIT_TCK_FAKE_BROKEN=",
			}
			rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{candidate})
			require.NoError(t, err)
			require.Len(t, rep.Findings, 1, "host-port refusal cannot count as ordering validation")
			require.Equal(t, report.Skip, rep.Findings[0].Severity, "%s", rep)
			require.Contains(t, rep.Findings[0].Detail, capHostPort)
			return
		}
	}
	t.Fatal("no prerequisite ordering check")
}

func TestOrderingTraceControlMayRefuseEngineElevation(t *testing.T) {
	for _, candidate := range checks {
		if candidate.requirement == "SPEC-v3 §7.1.2/no-partial-application" {
			rep := runAgainstFake(t, "ordering-refuses-engine", candidate)
			require.Len(t, rep.Findings, 1)
			require.Equal(t, report.Skip, rep.Findings[0].Severity, "%s", rep)
			return
		}
	}
	t.Fatal("no refusal ordering check")
}

func TestOrderingSelectionAndConflictRefusalHaveEffectTraces(t *testing.T) {
	for _, tc := range []struct{ broken, requirement string }{
		{"ordering-applies-before-selection", "SPEC-v3 §7.1.1/selection-before-application"},
		{"ordering-applies-before-conflict", "SPEC-v3 §7.1.1/conflicts-before-application"},
	} {
		t.Run(tc.broken, func(t *testing.T) {
			for _, candidate := range checks {
				if candidate.requirement == tc.requirement {
					rep := runAgainstFake(t, tc.broken, candidate)
					require.True(t, rep.Failed(), "selection/conflict coverage must observe premature effects: %s", rep)
					return
				}
			}
			t.Fatal("no selection or conflict refusal check")
		})
	}
}
