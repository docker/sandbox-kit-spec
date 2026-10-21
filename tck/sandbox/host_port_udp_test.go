package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
	"github.com/stretchr/testify/require"
)

func TestHostPortUDPChecks(t *testing.T) {
	t.Run("combined", func(t *testing.T) {
		rep := runAgainstFake(t, "", hostPortUDPChecks...)
		require.False(t, rep.Failed(), "%s", rep)
	})
	for _, c := range hostPortUDPChecks {
		t.Run(c.requirement, func(t *testing.T) {
			rep := runAgainstFake(t, "", c)
			require.False(t, rep.Failed(), "%s", rep)
		})
	}
}

// Exercise the shipped probe without the fake's socket wrapper, so a
// defect in that wrapper cannot conceal a broken workload executable.
func TestHostPortUDPGuestProbe(t *testing.T) {
	for _, mode := range []string{"single", "datagrams", "clients"} {
		t.Run(mode, func(t *testing.T) {
			service, err := newHostPortUDPService()
			require.NoError(t, err)
			defer service.start()()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", filepath.Join(FixtureDir, "workload", "kit-tck-host-port-udp.py"), service.port(), mode)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", out)
			require.NoError(t, hostPortUDPVerify(string(out), mode, service))
		})
	}
}

func TestHostPortUDPOnlyRuntime(t *testing.T) {
	a := adapter.New(filepath.Join("testdata", "fake-adapter"))
	a.Env = []string{"KIT_TCK_FAKE_STATE=" + t.TempDir(), "KIT_TCK_FAKE_CLAIMS=" + capHostPort, "KIT_TCK_FAKE_BROKEN="}
	rep, err := runChecks(t.Context(), &Env{Adapter: a, Fixtures: Fixtures(FixtureDir)}, hostPortUDPChecks)
	require.NoError(t, err)
	require.False(t, rep.Failed(), "%s", rep)
	for _, finding := range rep.Findings {
		if finding.Requirement == "host-port@1/udp-before-hooks" {
			require.Equal(t, report.Skip, finding.Severity)
		}
	}
}

func TestHostPortUDPNoGrantRequiresHostResponse(t *testing.T) {
	for _, mode := range []string{"host-port-udp-unrelated-listener", "host-port-udp-ungranted-access", "host-port-udp-active-ungranted-access"} {
		t.Run(mode, func(t *testing.T) {
			rep := runAgainstFake(t, mode, check{requirement: "host-port@1/udp-no-extra-access", capability: capHostPort, run: hostPortUDPNoGrant})
			require.Equal(t, mode != "host-port-udp-unrelated-listener", rep.Failed(), "%s", rep)
		})
	}
}

func TestHostPortUDPVerificationRejectsFabricatedObservations(t *testing.T) {
	service, err := newHostPortUDPService()
	require.NoError(t, err)
	defer func() { require.NoError(t, service.conn.Close()) }()
	require.Error(t, hostPortUDPVerify(`[]`, "single", service))
	data := make([]byte, 32)
	encoded := base64.StdEncoding.EncodeToString(data)
	require.Error(t, hostPortUDPVerify(fmt.Sprintf(`[[{"sent":%q,"received":%q}]]`, encoded, encoded), "single", service), "an echo is not evidence of the keyed host service")
}
