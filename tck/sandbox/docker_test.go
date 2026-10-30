package sandbox

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The image's own config file is still the image's config, in either
// spelling; any other file, and any spelling of a directive daemon.json
// can set, is refused.
func TestDaemonJSONViolation(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		ok   bool
	}{
		{nil, true},
		{[]string{"--containerd-namespace=moby"}, true},
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
		{[]string{"--group", "docker"}, false},
		{[]string{"--group=docker"}, false},
		{[]string{"-G", "docker"}, false},
		{[]string{"-Gdocker"}, false},
		{[]string{"-soverlay2"}, false},
		{[]string{"--storage-driver=overlay2"}, false},
		{[]string{"--debug"}, false},
		{[]string{"-D"}, false},
		{[]string{"--tlsverify"}, false},
		{[]string{"--pidfile=/run/docker.pid"}, false},
	} {
		got := daemonJSONViolation(tc.argv)
		if tc.ok {
			require.Empty(t, got, "%q", tc.argv)
		} else {
			require.NotEmpty(t, got, "%q", tc.argv)
		}
	}
}
