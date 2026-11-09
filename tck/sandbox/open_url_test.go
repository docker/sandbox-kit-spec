package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenURLRejectionProbe(t *testing.T) {
	for _, broken := range []string{"", "multiple", "credentials", "always-rejects", "tab", "newline", "control"} {
		t.Run(broken, func(t *testing.T) {
			dir := t.TempDir()
			helper := filepath.Join(dir, "xdg-open")
			require.NoError(t, os.WriteFile(helper, []byte(`#!/bin/sh
if [ "$BROKEN" = always-rejects ]; then exit 1; fi
if [ "$BROKEN" = tab ] || [ "$BROKEN" = newline ] || [ "$BROKEN" = control ]; then
  case "$BROKEN" in
    tab) byte=$(printf '\t') ;;
    newline) byte='
' ;;
    control) byte=$(printf '\001') ;;
  esac
  case "${1:-}" in *"$byte"*) exit 0 ;; esac
fi
if [ "$BROKEN" = multiple ] && [ "$#" -gt 1 ]; then
  case "$1" in http://*) exit 0 ;; esac
fi
if [ "$BROKEN" = credentials ] && [ "$#" = 1 ]; then
  case "$1" in *user:password*) exit 0 ;; esac
fi
if [ "$#" = 1 ]; then
  case "$1" in http://example.invalid/accepted | https://example.invalid/accepted) exit 0 ;; esac
fi
exit 1
`), 0700))
			cmd := exec.CommandContext(t.Context(), "sh", filepath.Join("testdata", "fixtures", "open-url-workload", "probe"), "invalid")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "BROWSER="+helper, "BROKEN="+broken)
			output, err := cmd.CombinedOutput()
			if broken == "" {
				require.NoError(t, err, "%s", output)
			} else {
				require.Error(t, err, "probe must detect %s acceptance", broken)
			}
		})
	}
}

func TestOpenURLProbeWithRateLimitedHelper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		budget int
		passes bool
	}{
		{name: "ordinary-one-per-window", budget: 1},
		{name: "insufficient-for-second-helper", budget: openURLProbeBudget / 2},
		{name: "reserved-test-budget", budget: openURLProbeBudget, passes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			helper := filepath.Join(dir, "xdg-open")
			require.NoError(t, os.WriteFile(helper, []byte(`#!/bin/sh
remaining=$(cat "$BUDGET_FILE")
[ "$remaining" -gt 0 ] || exit 1
printf '%s\n' "$((remaining - 1))" >"$BUDGET_FILE"
if [ "$#" = 1 ]; then
  case "$1" in http://example.invalid/accepted | https://example.invalid/accepted) exit 0 ;; esac
fi
exit 1
`), 0700))
			budgetFile := filepath.Join(dir, "budget")
			require.NoError(t, os.WriteFile(budgetFile, []byte(strconv.Itoa(tc.budget)), 0600))
			cmd := exec.CommandContext(t.Context(), "sh", filepath.Join("testdata", "fixtures", "open-url-workload", "probe"), "invalid")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "BROWSER="+helper, "BUDGET_FILE="+budgetFile)
			output, err := cmd.CombinedOutput()
			if tc.passes {
				require.NoError(t, err, "%s", output)
				remaining, err := os.ReadFile(budgetFile)
				require.NoError(t, err)
				require.Equal(t, "0\n", string(remaining), "budget must track every probe attempt")
			} else {
				require.Error(t, err, "insufficient budget must not hide a failed valid navigation")
			}
		})
	}
}

func TestOpenURLProbesPass(t *testing.T) {
	var selected []check
	for _, c := range checks {
		if c.capability == capOpenURL {
			selected = append(selected, c)
		}
	}
	require.Len(t, selected, 2)
	rep := runAgainstFake(t, "", selected...)
	require.False(t, rep.Failed(), "%s", rep)
}
