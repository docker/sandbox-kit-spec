package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateTransportsContainerEnvironmentLiterally(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$ARGV_FILE\"\nprintf 'sandbox-id\\n'\n"), 0700))
	argvFile := filepath.Join(dir, "argv")
	a := New(path)
	a.Env = []string{"ARGV_FILE=" + argvFile}
	value := "spaces = equals\nquotes'\" $HOME $(touch forbidden)"
	id, err := a.Create(t.Context(), []string{"workload", "mixin"}, CreateOptions{
		Args: map[string]string{"greeting": "argument"},
		Env:  map[string]string{"Z_VALUE": value, "A_EMPTY": ""},
	})
	require.NoError(t, err)
	require.Equal(t, "sandbox-id", id)
	raw, err := os.ReadFile(argvFile)
	require.NoError(t, err)
	require.Equal(t, []string{"create", "workload", "mixin", "--arg", "greeting=argument", "--env", "A_EMPTY=", "--env", "Z_VALUE=" + value}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
}

func TestRecreateWithPreservesLiteralArgumentsAndRefusalStatus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "adapter")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$ARGV_FILE\"\necho 'incompatible storage' >&2\nexit \"$EXIT_CODE\"\n"), 0700))
	argvFile := filepath.Join(dir, "argv")
	for _, code := range []string{"0", "1", "2"} {
		a := New(path)
		a.Env = []string{"ARGV_FILE=" + argvFile, "EXIT_CODE=" + code}
		value := "spaces $HOME $(touch forbidden)"
		err := a.RecreateWith(t.Context(), "instance", []string{"workload", "mixin"}, map[string]string{"size": value, "mode": ""})
		switch code {
		case "0":
			require.NoError(t, err)
		case "1":
			require.Error(t, err)
			var refused *RefusedError
			require.NotErrorAs(t, err, &refused)
		case "2":
			var refused *RefusedError
			require.ErrorAs(t, err, &refused)
		}
		raw, err := os.ReadFile(argvFile)
		require.NoError(t, err)
		require.Equal(t, []string{"recreate", "instance", "workload", "mixin", "--arg", "mode=", "--arg", "size=" + value}, strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00"))
	}
}

func TestVolumePathsRequiresAnArrayAndSuccessfulObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "adapter")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' \"$OUTPUT\"\nexit \"$EXIT_CODE\"\n"), 0700))
	for _, tc := range []struct {
		output, code string
		want         []string
		fail         bool
	}{
		{"[]", "0", []string{}, false},
		{`["/data"]`, "0", []string{"/data"}, false},
		{"null", "0", nil, true},
		{"{}", "0", nil, true},
		{"[]", "1", nil, true},
	} {
		a := New(path)
		a.Env = []string{"OUTPUT=" + tc.output, "EXIT_CODE=" + tc.code}
		got, err := a.VolumePaths(t.Context(), "instance")
		if tc.fail {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		}
	}
}
