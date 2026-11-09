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

func TestExactVersionRefusalsRequireCompleteIdentities(t *testing.T) {
	const legacy = "registry.example/tck/context:1"
	const modern = "registry.example/tck/context:10"
	const digest = "registry.example/tck/context@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name        string
		legacyRef   string
		modernRef   string
		detail      string
		unsupported bool
		wantFinding bool
	}{
		{name: "tag prefix omits lower source", detail: modern + " capabilities[0]", wantFinding: true},
		{name: "missing upper source", detail: legacy + " capabilities[0]", wantFinding: true},
		{name: "both tags", detail: legacy + " capabilities[0] conflicts with " + modern + " capabilities[0]"},
		{name: "quoted tags and punctuation", detail: "[\"" + legacy + "\", \"" + modern + "\"] (capabilities[0])."},
		{name: "key value diagnostic", detail: "kit=" + legacy + " path=capabilities[0] conflicts with kit=" + modern + " path=capabilities[0]"},
		{name: "lookalike before complete source", detail: modern + " resembles " + legacy + " capabilities[0] conflicts with " + modern + " capabilities[0]"},
		{name: "source followed by colon", detail: legacy + ": capabilities[0] conflicts with " + modern + ": capabilities[0]"},
		{name: "source followed by period", detail: "capabilities[0] in " + legacy + ". Conflicts with capabilities[0] in " + modern + "."},
		{name: "repository suffix", legacyRef: "registry.example/tck/context", modernRef: "registry.example/tck/context/child", detail: "registry.example/tck/context/child capabilities[0]", wantFinding: true},
		{name: "repository prefix", modernRef: "other/" + legacy, detail: "other/" + legacy + " capabilities[0]", wantFinding: true},
		{name: "tagged source versus pinned source", modernRef: legacy + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", detail: legacy + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa capabilities[0]", wantFinding: true},
		{name: "both tagged and pinned sources", modernRef: legacy + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", detail: legacy + " capabilities[0] conflicts with " + legacy + "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa capabilities[0]"},
		{name: "digest repository prefix", legacyRef: digest, modernRef: "other/" + digest, detail: "other/" + digest + " capabilities[0]", wantFinding: true},
		{name: "digest suffix", legacyRef: digest, modernRef: digest + "b", detail: digest + "b capabilities[0]", wantFinding: true},
		{name: "both digest references", legacyRef: digest, modernRef: "other/" + digest, detail: "'" + digest + "' capabilities[0] conflicts with 'other/" + digest + "' capabilities[0]"},
		{name: "fixture suffix", detail: "exact-context-v1-optional-extra capabilities[0] conflicts with exact-context-v2-optional capabilities[0]", wantFinding: true},
		{name: "fixture prefix", detail: "other-exact-context-v1-optional capabilities[0] conflicts with exact-context-v2-optional capabilities[0]", wantFinding: true},
		{name: "both fixture names", detail: "[exact-context-v1-optional] capabilities[0] conflicts with (exact-context-v2-optional) capabilities[0]"},
		{name: "capability version prefix", unsupported: true, detail: legacy + " capabilities[0] unsupported com.docker.sandbox/agent-context@10", wantFinding: true},
		{name: "capability namespace prefix", unsupported: true, detail: legacy + " capabilities[0] unsupported other/" + capAgentContext, wantFinding: true},
		{name: "complete unsupported capability", unsupported: true, detail: legacy + " capabilities[0] unsupported '" + capAgentContext + "'."},
		{name: "more precise location", detail: legacy + " capabilities[0].config conflicts with " + modern + " capabilities[0].config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := filepath.Join(t.TempDir(), "adapter")
			require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$DETAIL\" >&2\nexit 2\n"), 0700))
			a := adapter.New(stub)
			a.Env = []string{"DETAIL=" + tc.detail}
			if tc.legacyRef == "" {
				tc.legacyRef = legacy
			}
			if tc.modernRef == "" {
				tc.modernRef = modern
			}
			fixtures := func(name string) string {
				if strings.Contains(name, "-v1-") {
					return tc.legacyRef
				}
				return tc.modernRef
			}
			authored := []exactContextRequest{{1, true}, {2, true}}
			claimed := map[string]bool{capAgentContext: true, capAgentContextV2: true}
			if tc.unsupported {
				authored = []exactContextRequest{{1, false}}
				claimed = nil
			}
			findings := exactContextSelection(t.Context(), &Env{Adapter: a, Fixtures: fixtures, Claimed: claimed}, authored, nil, "")
			if tc.wantFinding {
				require.NotEmpty(t, findings, "refusal must name each complete request identity")
			} else {
				require.Empty(t, findings, "complete identities may use ordinary diagnostic punctuation")
			}
		})
	}
}
