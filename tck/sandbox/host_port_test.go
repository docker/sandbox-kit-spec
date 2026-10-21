package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

func TestHostPortChecks(t *testing.T) {
	t.Run("combined", func(t *testing.T) {
		rep := runAgainstFake(t, "", hostPortChecks...)
		require.False(t, rep.Failed(), "%s", rep)
	})
	for _, c := range hostPortChecks {
		t.Run(c.requirement, func(t *testing.T) {
			rep := runAgainstFake(t, "", c)
			require.False(t, rep.Failed(), "%s", rep)
		})
	}
}

func TestHostPortOnlyRuntime(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capHostPort, "KIT_TCK_FAKE_BROKEN="}
	rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, hostPortChecks)
	require.NoError(t, err)
	require.False(t, rep.Failed(), "%s", rep)
	for _, finding := range rep.Findings {
		if finding.Requirement == "host-port@1/before-hooks" {
			require.Equal(t, report.Skip, finding.Severity)
		}
	}
}

func TestFakeHostPortRejectsUndeclaredOverrides(t *testing.T) {
	for _, tc := range []struct{ fixture, argument string }{
		{"host-port", "default_host_port"},
		{"host-port-hooks", "default_host_port"},
		{"host-port-default", "container_port"},
		{"host-port-udp", "default_host_port"},
		{"host-port-udp-hooks", "default_host_port"},
		{"host-port-udp-default", "container_port"},
		{"host-port-udp-alias", "container_port"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capHostPort, "KIT_TCK_FAKE_BROKEN="}
			_, err := a.Create(t.Context(), []string{Fixtures(FixtureDir)(tc.fixture)}, adapter.CreateOptions{
				Args: map[string]string{tc.argument: "18432"}, RejectCapabilities: []string{capHostPort},
			})
			require.ErrorContains(t, err, "undeclared argument override")
			var refused *adapter.RefusedError
			require.NotErrorAs(t, err, &refused, "invalid arguments must not satisfy capability-refusal checks")
		})
	}
}

func TestHostPortNoGrantRequiresHostResponse(t *testing.T) {
	for _, mode := range []string{"host-port-unrelated-listener", "host-port-denial-response", "host-port-ungranted-access"} {
		t.Run(mode, func(t *testing.T) {
			rep := runAgainstFake(t, mode, check{
				requirement: "host-port@1/no-extra-access", capability: capHostPort, run: hostPortNoGrant,
			})
			require.Equal(t, mode == "host-port-ungranted-access", rep.Failed(), "%s", rep)
		})
	}
}

func TestHostPortNoGrantWithActiveMapping(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capHostPort, "KIT_TCK_FAKE_BROKEN=host-port-active-ungranted-access"}
	e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}
	s, err := newHostPortService()
	require.NoError(t, err)
	defer s.start()()

	id, remove, err := e.sandbox(t.Context(), []string{fixtureWorkload}, nil)
	require.NoError(t, err)
	defer remove()
	require.Empty(t, hostPortUngranted(t.Context(), e, id, s), "mutation must preserve isolation when no mapping exists")

	findings := hostPortNoGrant(t.Context(), e)
	require.Len(t, findings, 1)
	require.Equal(t, report.Fail, findings[0].Severity)
	require.Contains(t, findings[0].Detail, "host service reachable without a grant")
}

func TestHostPortNoGrantObservesOutboundOnlyLeaks(t *testing.T) {
	for _, mode := range []string{"host-port-outbound-only", "host-port-active-outbound-only", "host-port-udp-outbound-only", "host-port-udp-active-outbound-only"} {
		t.Run(mode, func(t *testing.T) {
			a := adapter.New(filepath.Join("testdata", "fake-adapter"))
			a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capHostPort, "KIT_TCK_FAKE_BROKEN=" + mode}
			e := &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}
			var findings []report.Finding
			if strings.Contains(mode, "udp") {
				findings = hostPortUDPNoGrant(t.Context(), e)
			} else {
				findings = hostPortNoGrant(t.Context(), e)
			}
			require.Len(t, findings, 1)
			require.Contains(t, findings[0].Detail, "received an ungranted request", "discarded replies must not conceal host-side effects")
		})
	}
}
