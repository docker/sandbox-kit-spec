package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
)

func TestExactVersionSelectionForPartialRuntimes(t *testing.T) {
	for _, claims := range [][]string{{capAgentContext}, {capAgentContextV2}, {capAgentContext, capAgentContextV2}, {groupVolume}} {
		t.Run(strings.Join(claims, ","), func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + strings.Join(claims, ","), "KIT_TCK_FAKE_BROKEN="}
			rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, []check{{requirement: exactVersionRequirement, run: exactVersionSelection}})
			require.NoError(t, err)
			require.False(t, rep.Failed(), "%s", rep)
			require.Empty(t, rep.Findings, "the core selection check must run for every support combination")
		})
	}
}

func TestExactVersionRefusalsAcceptMappedFixtureReferences(t *testing.T) {
	for _, tc := range []struct {
		name     string
		authored []exactContextRequest
		claimed  map[string]bool
		rejected []string
		pair     string
		detail   string
	}{
		{name: "unsupported required", authored: []exactContextRequest{{1, false}}, detail: "registry.example/tck/kit:legacy capabilities[0] unsupported " + capAgentContext},
		{name: "rejected required", authored: []exactContextRequest{{1, false}}, claimed: map[string]bool{capAgentContext: true}, rejected: []string{capAgentContext}, detail: "registry.example/tck/kit:legacy capabilities[0] policy rejected"},
		{name: "independent conflict", authored: []exactContextRequest{{1, true}, {2, true}}, claimed: map[string]bool{capAgentContext: true, capAgentContextV2: true}, detail: "registry.example/tck/kit:legacy capabilities[0] conflicts with registry.example/tck/kit:modern capabilities[0]"},
		{name: "pair conflict", authored: []exactContextRequest{{1, true}, {2, true}}, claimed: map[string]bool{capAgentContext: true, capAgentContextV2: true}, pair: "exact-context-pair-optional", detail: "registry.example/tck/kit:pair capabilities[0] conflicts with capabilities[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := filepath.Join(t.TempDir(), "adapter")
			require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\n' \"$DETAIL\" >&2\nexit 2\n"), 0700))
			a := adapter.New(stub)
			a.Env = []string{"DETAIL=" + tc.detail}
			fixtures := func(name string) string {
				tag := "base"
				if strings.Contains(name, "-v1-") {
					tag = "legacy"
				}
				if strings.Contains(name, "-v2-") {
					tag = "modern"
				}
				if strings.Contains(name, "-pair-") {
					tag = "pair"
				}
				return "registry.example/tck/kit:" + tag
			}
			e := &Env{Adapter: a, Fixtures: fixtures, Claimed: tc.claimed}
			require.Empty(t, exactContextSelection(t.Context(), e, tc.authored, tc.rejected, tc.pair))
		})
	}
}
