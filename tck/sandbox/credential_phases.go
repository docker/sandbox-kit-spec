package sandbox

import (
	"context"
	"strings"
	"time"

	"github.com/docker/sandbox-kit-spec/v3/tck/report"
)

// Named, proxy-managed API-key sentinels are tested in each phase and both
// together. Their environments cannot prove that outbound injection or OAuth
// presentations obey the same boundary; those need independent probes.
func credentialSentinelPhaseScoped(suffix string) func(context.Context, *Env) []report.Finding {
	return func(ctx context.Context, e *Env) []report.Finding {
		for _, tc := range []struct {
			fixture          string
			install, runtime bool
		}{
			{"credential-phases-install", true, false},
			{"credential-phases-runtime", false, true},
			{"credential-phases-both", true, true},
		} {
			tc.fixture += suffix
			findings := func() []report.Finding {
				id, cleanup, err := e.sandbox(ctx, []string{fixtureWorkload, tc.fixture}, nil)
				if err != nil {
					return []report.Finding{report.Failf("create %s: %v", tc.fixture, err)}
				}
				defer cleanup()
				install, f := execOutput(ctx, e, id, "cat", "/var/tmp/credential-at-install")
				if f != nil {
					return []report.Finding{*f}
				}
				entrypoint, f := credentialEntrypoint(ctx, e, id)
				if f != nil {
					return []report.Finding{*f}
				}
				runtime, err := e.Adapter.Exec(ctx, id, "printenv", "KIT_TCK_PHASE_TOKEN")
				if err != nil {
					return []report.Finding{report.Failf("read runtime credential: %v", err)}
				}
				if runtime.ExitCode != 0 && (tc.runtime || runtime.ExitCode != 1) {
					return []report.Finding{report.Failf("%s: runtime credential probe exited %d", tc.fixture, runtime.ExitCode)}
				}
				for _, phase := range []struct {
					name, value string
					granted     bool
				}{{"install", install, tc.install}, {"workload entrypoint", entrypoint, tc.runtime}, {"runtime exec", runtime.Stdout, tc.runtime}} {
					value := strings.TrimSpace(phase.value)
					if (value != "") != phase.granted {
						return []report.Finding{report.Failf("%s: credential presence during %s does not match its phase grant", tc.fixture, phase.name)}
					}
					if strings.Contains(value, e.Secret) {
						return []report.Finding{report.Failf("%s: real secret reached the %s phase instead of a sentinel", tc.fixture, phase.name)}
					}
				}
				return nil
			}()
			if len(findings) != 0 {
				return findings
			}
		}
		return nil
	}
}

// Create may return before the workload starts. Wait for its own atomic
// record: a later exec's environment cannot prove what the entrypoint saw.
func credentialEntrypoint(ctx context.Context, e *Env, id string) (string, *report.Finding) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		value, f := execOutput(ctx, e, id, "cat", "/var/tmp/kit-tck-workload-credential")
		if f == nil {
			return value, nil
		}
		select {
		case <-ctx.Done():
			return "", failing("read workload entrypoint's credential record: %s", f.Detail)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
