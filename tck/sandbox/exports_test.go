package sandbox

import (
	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInjectOnlyFixtureExportsAreNotCredentialVariables(t *testing.T) {
	exports, err := fixtureEnvironmentExports(fixtureCredentialInjectOnlyInstall)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"DECLARED": "yes"}, exports)
}

func TestInjectOnlyCheckJudgesFixtureExportValues(t *testing.T) {
	const requirement = "credential@1/inject-only-no-env"
	var selected check
	for _, candidate := range checks {
		if candidate.requirement == requirement {
			selected = candidate
			break
		}
	}
	require.NotEmpty(t, selected.requirement)
	for _, resolver := range []struct {
		name    string
		fixture func(string) string
	}{
		{"local directory", Fixtures(FixtureDir)},
		{"registry reference", func(name string) string { return "registry.example/suite/" + name + ":1" }},
	} {
		t.Run(resolver.name, func(t *testing.T) {
			for _, mutation := range []string{"", "hides-inject-only-in-export"} {
				name := "declared export"
				if mutation != "" {
					name = "sentinel in declared export"
				}
				t.Run(name, func(t *testing.T) {
					a := adapter.New(filepath.Join("testdata", "fake-adapter"))
					a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_BROKEN=" + mutation, "KIT_TCK_FAKE_CLAIMS="}
					rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: resolver.fixture}, []check{selected})
					require.NoError(t, err)
					if mutation == "" {
						require.False(t, rep.Failed(), "%s", rep)
					} else {
						require.Contains(t, failedRequirements(rep), requirement)
						require.Len(t, rep.Findings, 1)
						require.Contains(t, rep.Findings[0].Detail, "added environment variables [DECLARED]")
					}
				})
			}
		})
	}
}
