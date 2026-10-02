package adapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostMountListingRejectsAmbiguousCleanupHandles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$RESPONSE\"\n"), 0o700))
	for _, response := range []string{
		`null`, `not JSON`,
		`[{"id":"one","path":"/cache","hostPath":""}]`,
		`[{"id":"one","path":"/cache/","hostPath":"/host/cache"}]`,
		`[{"id":"one","path":"/cache","hostPath":"/host/cache"},{"id":"one","path":"/other","hostPath":"/host/other"}]`,
		`[{"id":"one","path":"/cache","hostPath":"/host/cache"},{"id":"two","path":"/cache","hostPath":"/host/other"}]`,
	} {
		a := New(path)
		a.Env = []string{"RESPONSE=" + response}
		_, err := a.HostMounts(t.Context(), "kit")
		require.Error(t, err, "listing %s", response)
	}
}
