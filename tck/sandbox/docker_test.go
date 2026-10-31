package sandbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The image's own config file is the only argument the engine may get, in
// either spelling; any other argument is a setting daemon.json carries.
func TestDaemonJSONViolation(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		ok   bool
	}{
		{nil, true},
		{[]string{"--config-file", "/etc/docker/daemon.json"}, true},
		{[]string{"--config-file=/etc/docker/daemon.json"}, true},
		{[]string{"--config-file=/etc/docker/./daemon.json"}, true},
		{[]string{"--config-file", "/run/kit/daemon.json"}, false},
		{[]string{"--config-file=/run/kit/daemon.json"}, false},
		{[]string{"--config-file"}, false},
		{[]string{"--config-file", "/etc/docker/daemon.json", "--debug"}, false},
		{[]string{"--debug"}, false},
		{[]string{"-D"}, false},
		{[]string{"--group=docker"}, false},
		{[]string{"--group", "docker"}, false},
		{[]string{"-G", "docker"}, false},
		{[]string{"-Gdocker"}, false},
		{[]string{"--log-level=debug"}, false},
		{[]string{"-H", "unix:///var/run/docker.sock"}, false},
		{[]string{"-Hunix:///var/run/docker.sock"}, false},
		{[]string{"--host=unix:///var/run/docker.sock"}, false},
		{[]string{"--data-root", "/var/lib/docker"}, false},
		{[]string{"--containerd-namespace=moby"}, false},
		{[]string{"positional"}, false},
	} {
		got := daemonJSONViolation(tc.argv)
		if tc.ok {
			require.Empty(t, got, "%q", tc.argv)
		} else {
			require.NotEmpty(t, got, "%q", tc.argv)
		}
	}
}
