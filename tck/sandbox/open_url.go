package sandbox

import (
	"context"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/adapter"
	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

const capOpenURL = "com.docker.sandbox/open-url@1"

// Each helper receives two valid requests and twelve invalid attempts.
// Reserve attempts, not launches, so invalid input cannot exhaust a
// runtime's admission budget before the second helper is exercised.
const openURLProbeBudget = 28

func openURLGuestHelper(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandbox(ctx, []string{"open-url-workload", "open-url"}, nil)
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("create: %v", err)}
	}
	_, f := execOutput(ctx, e, id, "kit-tck-open-url", "interface")
	if f != nil {
		return []report.Finding{*f}
	}
	// Create may return before the entrypoint publishes its capture.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		result, f := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-open-url-interface")
		if f == nil {
			if strings.TrimSpace(result) != "0" {
				return []report.Finding{report.Failf("workload helper interface probe failed: %s", result)}
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return []report.Finding{report.Failf("read workload interface capture: %s", f.Detail)}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func openURLRejectsInvalid(ctx context.Context, e *Env) []report.Finding {
	id, cleanup, err := e.sandboxWith(ctx, []string{"open-url-workload", "open-url"}, adapter.CreateOptions{OpenURLIsolated: true, OpenURLBudget: openURLProbeBudget})
	defer cleanup()
	if err != nil {
		return []report.Finding{report.Failf("create with isolated host opener: %v", err)}
	}
	_, f := execOutput(ctx, e, id, "kit-tck-open-url", "invalid")
	if f != nil {
		return []report.Finding{*f}
	}
	return nil
}
