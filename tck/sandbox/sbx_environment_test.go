package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

func TestSbxWorkspaceSkipRequiresAnExplicitUnavailableMode(t *testing.T) {
	for _, tc := range []struct {
		err  error
		mode string
		want bool
	}{
		{&adapter.RefusedError{Detail: "unsupported workspace mode: none"}, "none", true},
		{&adapter.RefusedError{Detail: "unsupported workspace mode: none"}, "internal", false},
		{&adapter.RefusedError{Detail: "missing environment variable WORKSPACE_DIR"}, "none", false},
		{fmt.Errorf("adapter crashed"), "none", false},
		{nil, "none", false},
		{&adapter.RefusedError{Detail: "unsupported workspace mode: "}, "", false},
	} {
		require.Equal(t, tc.want, sbxWorkspaceUnavailable(tc.err, tc.mode))
	}
}

func TestSbxEnvironmentDefaultWorkspaceAndPartialClaims(t *testing.T) {
	for _, mode := range []string{"internal", "none"} {
		for _, claims := range []string{capSbx, capSbx + "," + capLifecycle} {
			t.Run(mode+"/"+claims, func(t *testing.T) {
				a := adapter.New(filepath.Join("testdata", "fake-adapter"))
				a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + claims, "KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_DEFAULT_WORKSPACE=" + mode}
				var selected []check
				for _, c := range checks {
					if strings.HasPrefix(c.requirement, "sbx@1/environment-") || strings.HasPrefix(c.requirement, "sbx@1/identity-") {
						selected = append(selected, c)
					}
				}
				rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, selected)
				require.NoError(t, err)
				require.False(t, rep.Failed(), "%s", rep)
				for _, finding := range rep.Findings {
					require.Equal(t, report.Skip, finding.Severity)
					require.Contains(t, finding.Requirement, "environment-hooks", "only hook observations depend on lifecycle")
					require.Equal(t, capSbx, claims, "full claim should judge every new check")
				}
			})
		}
	}
}
