package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

func TestFailedCreateCleansAndReportsRetainedStorage(t *testing.T) {
	// Cancellation waits for the adapter to allocate before interrupting
	// it. Keep these short cases ahead of the parallel mutation suite so
	// process contention cannot exhaust that startup wait.
	for _, outcome := range []string{"1", "2", "cancel"} {
		for _, cleanupFails := range []bool{false, true} {
			label := outcome + "/removed"
			if cleanupFails {
				label = outcome + "/cleanup-fails"
			}
			t.Run(label, func(t *testing.T) {
				dir := t.TempDir()
				executable := filepath.Join(dir, "adapter")
				allocation := filepath.Join(dir, "allocation")
				created := filepath.Join(dir, "created-name")
				removed := filepath.Join(dir, "removed-name")
				require.NoError(t, os.WriteFile(executable, []byte(`#!/bin/sh
case "$1" in
create)
  shift
  while [ "$#" -gt 0 ]; do
    if [ "$1" = --name ]; then
      printf '%s\n' "$2" > "$CREATED_NAME"
      shift
    fi
    shift
  done
  : > "$ALLOCATION"
  if [ "$CREATE_OUTCOME" = cancel ]; then exec sleep 60; fi
  echo 'create failed after allocation' >&2
  exit "$CREATE_OUTCOME"
  ;;
volume-rm)
  printf '%s\n' "$2" >> "$REMOVED_NAME"
  if [ "$CLEANUP_FAILS" = true ]; then
    echo 'storage removal failed' >&2
    exit 1
  fi
  rm "$ALLOCATION"
  ;;
*) exit 1 ;;
esac
`), 0700))
				a := adapter.New(executable)
				failSetting := "false"
				if cleanupFails {
					failSetting = "true"
				}
				a.Env = []string{
					"ALLOCATION=" + allocation, "CREATED_NAME=" + created, "REMOVED_NAME=" + removed,
					"CREATE_OUTCOME=" + outcome, "CLEANUP_FAILS=" + failSetting,
				}
				e := &Env{Adapter: a, Fixtures: func(name string) string { return name }, Claimed: map[string]bool{capVolume: true}}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				type result struct {
					report report.Report
					err    error
				}
				finished := make(chan result, 1)
				go func() {
					rep, err := runChecks(ctx, e, []check{{requirement: "test/failed-create", run: func(ctx context.Context, e *Env) []report.Finding {
						_, cleanup, err := e.sandboxWith(ctx, []string{"workload"}, adapter.CreateOptions{})
						cleanup()
						if err == nil {
							return []report.Finding{report.Failf("control create unexpectedly passed")}
						}
						return nil
					}}})
					finished <- result{rep, err}
				}()
				if outcome == "cancel" {
					require.Eventually(t, func() bool {
						_, err := os.Stat(allocation)
						return err == nil
					}, 5*time.Second, 10*time.Millisecond)
					cancel()
				}
				var got result
				select {
				case got = <-finished:
				case <-time.After(10 * time.Second):
					t.Fatal("create cleanup did not finish after failure or cancellation")
				}
				if outcome == "cancel" {
					require.ErrorIs(t, got.err, context.Canceled)
				} else {
					require.NoError(t, got.err)
				}
				createdName, err := os.ReadFile(created)
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(string(createdName), "kit-tck-"))
				removedName, err := os.ReadFile(removed)
				require.NoError(t, err)
				require.Equal(t, string(createdName), string(removedName), "cleanup targets the allocated name exactly once")
				if cleanupFails {
					require.FileExists(t, allocation)
					require.Equal(t, []string{"conformance.md §2.4"}, failedRequirements(got.report))
					require.Equal(t, "cleanup", got.report.Findings[0].Check)
					require.Contains(t, got.report.Findings[0].Detail, strings.TrimSpace(string(createdName)))
				} else {
					require.NoFileExists(t, allocation)
					require.Empty(t, got.report.Findings)
				}
			})
		}
	}
}
