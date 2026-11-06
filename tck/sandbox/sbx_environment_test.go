package sandbox

import (
	"fmt"
	"os"
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

func TestSbxWorkspacePlacementRefusal(t *testing.T) {
	for _, tc := range []struct {
		detail string
		want   report.Severity
	}{
		{"unsupported workspace mode: internal", report.Skip},
		{"unsupported workspace mode: none", report.Fail},
		{"missing environment variable WORKSPACE_DIR", report.Fail},
	} {
		t.Run(tc.detail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "adapter")
			require.NoError(t, os.WriteFile(path, []byte(`#!/bin/sh
case "$1" in
capabilities) printf '%s\n' com.docker.sandbox/sbx@1 com.docker.sandbox/agent-context@1 ;;
create) printf '%s\n' "$KIT_TCK_TEST_REFUSAL" >&2; exit 2 ;;
esac
`), 0o755))
			a := adapter.New(path)
			a.Env = []string{"KIT_TCK_TEST_REFUSAL=" + tc.detail}
			for _, c := range checks {
				if c.requirement != "sbx@1/workspace-at-workdir" {
					continue
				}
				rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{c})
				require.NoError(t, err)
				require.Len(t, rep.Findings, 1)
				require.Equal(t, tc.want, rep.Findings[0].Severity, "%s", rep)
			}
		})
	}
}

func TestSbxEnvironmentDefaultWorkspaceAndPartialClaims(t *testing.T) {
	for _, mode := range []string{"internal", "none"} {
		for _, claims := range []string{capSbx, capSbx + "," + capLifecycle, capSbx + "," + capAgentContext, capSbx + "," + capLifecycle + "," + capAgentContext} {
			t.Run(mode+"/"+claims, func(t *testing.T) {
				a := adapter.New(filepath.Join("testdata", "fake-adapter"))
				a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + claims, "KIT_TCK_FAKE_BROKEN=", "KIT_TCK_FAKE_DEFAULT_WORKSPACE=" + mode}
				var selected []check
				for _, c := range checks {
					if strings.HasPrefix(c.requirement, "sbx@1/environment-") || strings.HasPrefix(c.requirement, "sbx@1/identity-") || c.requirement == "sbx@1/workspace-at-workdir" {
						selected = append(selected, c)
					}
				}
				rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, selected)
				require.NoError(t, err)
				require.False(t, rep.Failed(), "%s", rep)
				for _, finding := range rep.Findings {
					require.Equal(t, report.Skip, finding.Severity)
					if finding.Requirement == "sbx@1/workspace-at-workdir" {
						require.NotContains(t, claims, capAgentContext, "workspace observation needs agent-context")
					} else {
						require.Contains(t, finding.Requirement, "environment-hooks", "only hook observations depend on lifecycle")
						require.NotContains(t, claims, capLifecycle, "hook observations should run when lifecycle is claimed")
					}
				}
			})
		}
	}
}
