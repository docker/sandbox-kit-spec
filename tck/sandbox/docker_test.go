package sandbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The image's own config file is still the image's config, in either
// spelling; any other file, and any spelling of a directive the fixture's
// daemon.json sets, is refused.
func TestDaemonJSONViolation(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		ok   bool
	}{
		{nil, true},
		{[]string{"--debug"}, true},
		{[]string{"--config-file", "/etc/docker/daemon.json"}, true},
		{[]string{"--config-file=/etc/docker/daemon.json"}, true},
		{[]string{"--config-file=/etc/docker/./daemon.json"}, true},
		{[]string{"--config-file", "/run/kit/daemon.json"}, false},
		{[]string{"--config-file=/run/kit/daemon.json"}, false},
		{[]string{"--config-file"}, false},
		{[]string{"-H", "unix:///var/run/docker.sock"}, false},
		{[]string{"-Hunix:///var/run/docker.sock"}, false},
		{[]string{"--host=unix:///var/run/docker.sock"}, false},
		{[]string{"--data-root", "/var/lib/docker"}, false},
		{[]string{"-g/var/lib/docker"}, false},
		{[]string{"--graph=/var/lib/docker"}, false},
	} {
		got := daemonJSONViolation(tc.argv)
		if tc.ok {
			require.Empty(t, got, "%q", tc.argv)
		} else {
			require.NotEmpty(t, got, "%q", tc.argv)
		}
	}
}
