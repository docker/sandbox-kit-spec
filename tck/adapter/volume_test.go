package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVolumeStorageRequiresValidAllocationPaths(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "adapter")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' \"$OUTPUT\"\nexit \"$EXIT_CODE\"\n"), 0700))
	for _, tc := range []struct {
		output, code string
		fail         bool
	}{
		{`[]`, "0", false},
		{`["/data","/cache"]`, "0", false},
		{`null`, "0", true},
		{`{}`, "0", true},
		{`["relative"]`, "0", true},
		{`["/data/../cache"]`, "0", true},
		{`["/data","/data"]`, "0", true},
		{`["/data\u0000"]`, "0", true},
		{`[]`, "1", true},
	} {
		t.Run(tc.output+tc.code, func(t *testing.T) {
			a := New(executable)
			a.Env = []string{"OUTPUT=" + tc.output, "EXIT_CODE=" + tc.code}
			_, err := a.VolumeStoragePaths(t.Context(), "sandbox")
			if tc.fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVolumeRemovalTransportsNameAndPathLiterally(t *testing.T) {
	dir := t.TempDir()
	executable, args := filepath.Join(dir, "adapter"), filepath.Join(dir, "args")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$ARGV_FILE\"\n"), 0700))
	a := New(executable)
	a.Env = []string{"ARGV_FILE=" + args}
	name, destination := "sandbox with spaces", "/data/quotes'\" $HOME $(touch forbidden)"
	require.NoError(t, a.RemoveVolumeStorage(t.Context(), name, destination))
	raw, err := os.ReadFile(args)
	require.NoError(t, err)
	require.Equal(t, []string{"volume-rm", name, destination}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
	require.NoError(t, a.RemoveVolumeStorage(t.Context(), name, ""))
	raw, err = os.ReadFile(args)
	require.NoError(t, err)
	require.Equal(t, []string{"volume-rm", name}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
}
