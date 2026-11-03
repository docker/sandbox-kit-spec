package sandbox

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDockerAuthProbeCompatibility(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "python3", "testdata/fixtures/workload/kit-tck-docker-auth_test.py")
	cmd.Env = append(cmd.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestDockerAuthClientCompatibility(t *testing.T) {
	for mode, requirements := range map[string][]string{
		"docker-auth-renews-token":        {"bearer-scope"},
		"docker-auth-access-token-only":   {"token-mediation", "lifetime"},
		"docker-auth-relative-location":   {"redirect-confinement"},
		"docker-auth-host-redirect":       {"redirect-confinement"},
		"docker-auth-helper-config":       {"host-only", "host-resolution"},
		"docker-auth-url-config":          {"host-only", "host-resolution"},
		"docker-auth-rewritten-realm":     {"host-resolution", "lifetime"},
		"docker-auth-extra-workload-skip": {"host-resolution"},
	} {
		t.Run(mode, func(t *testing.T) {
			var selected []check
			for _, c := range checks {
				for _, name := range requirements {
					if c.requirement == "docker-auth@1/"+name {
						selected = append(selected, c)
					}
				}
			}
			require.Len(t, selected, len(requirements))
			rep := runAgainstFake(t, mode, selected...)
			require.False(t, rep.Failed(), "%s", rep)
		})
	}
}

func TestDockerAuthConformingRuntime(t *testing.T) {
	var selected []check
	for _, c := range checks {
		if c.capability == capDockerAuth {
			selected = append(selected, c)
		}
	}
	require.Len(t, selected, 8)
	rep := runAgainstFake(t, "", selected...)
	require.False(t, rep.Failed(), "%s", rep)
}
